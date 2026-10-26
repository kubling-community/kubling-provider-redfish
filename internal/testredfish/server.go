// Package testredfish provides synthetic TLS Redfish services for tests.
package testredfish

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	serviceRootPath = "/redfish/v1/"
	sessionsPath    = "/redfish/v1/SessionService/Sessions"
)

// Authentication selects the authentication behavior of a synthetic service.
type Authentication string

const (
	AuthenticationSession Authentication = "session"
	AuthenticationBasic   Authentication = "basic"
)

// Options configures one isolated synthetic Redfish service.
type Options struct {
	ID                  string
	Username            string
	Password            string
	Authentication      Authentication
	SessionDeleteStatus int
	ServiceRoot         map[string]any
	Resources           map[string]json.RawMessage
	Routes              map[string]http.Handler
}

// Request is a sanitized request observation.
type Request struct {
	Method         string
	Path           string
	Authentication string
}

// Server is one TLS Redfish simulator with its own credentials and state.
type Server struct {
	server              *httptest.Server
	caFile              string
	id                  string
	username            string
	password            string
	auth                Authentication
	token               string
	sessionDeleteStatus int
	resources           map[string]json.RawMessage
	routes              map[string]http.Handler
	serviceRoot         map[string]any
	mu                  sync.Mutex
	requests            []Request
	sessionAlive        bool
}

// NewServer starts an isolated TLS Redfish simulator.
func NewServer(t testing.TB, options Options) *Server {
	t.Helper()
	if options.ID == "" || options.Username == "" || options.Password == "" {
		t.Fatal("test Redfish server ID and credentials are required")
	}
	if options.Authentication != AuthenticationSession && options.Authentication != AuthenticationBasic {
		t.Fatalf("unsupported test Redfish authentication %q", options.Authentication)
	}
	resources := make(map[string]json.RawMessage, len(options.Resources))
	for path, body := range options.Resources {
		resources[path] = append(json.RawMessage(nil), body...)
	}
	routes := make(map[string]http.Handler, len(options.Routes))
	for path, handler := range options.Routes {
		routes[path] = handler
	}
	result := &Server{
		id:                  options.ID,
		username:            options.Username,
		password:            options.Password,
		auth:                options.Authentication,
		token:               "synthetic-token-" + options.ID,
		sessionDeleteStatus: options.SessionDeleteStatus,
		resources:           resources,
		routes:              routes,
		serviceRoot:         cloneObject(options.ServiceRoot),
	}
	certificate, caDER := newCertificate(t, options.ID)
	result.server = httptest.NewUnstartedServer(http.HandlerFunc(result.serveHTTP))
	result.server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	result.server.StartTLS()
	caBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(t.TempDir(), "redfish-test-ca.pem")
	if err := os.WriteFile(caFile, caBytes, 0o600); err != nil {
		result.server.Close()
		t.Fatalf("write test CA: %v", err)
	}
	result.caFile = caFile
	t.Cleanup(result.Close)
	return result
}

func newCertificate(t testing.TB, id string) (tls.Certificate, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test CA key: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test server key: %v", err)
	}
	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("generate test CA serial: %v", err)
	}
	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("generate test leaf serial: %v", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Kubling synthetic Redfish CA " + id},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create test CA certificate: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject:      pkix.Name{CommonName: "Kubling synthetic Redfish " + id},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create test server certificate: %v", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
	}, caDER
}

// Endpoint returns the TLS service origin.
func (s *Server) Endpoint() string {
	return s.server.URL
}

// CAFile returns a PEM trust anchor for this server only.
func (s *Server) CAFile() string {
	return s.caFile
}

// Close stops the simulator.
func (s *Server) Close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}

// String formats only the synthetic server ID and endpoint.
func (s *Server) String() string {
	if s == nil || s.server == nil {
		return "synthetic Redfish server (nil)"
	}
	return fmt.Sprintf("synthetic Redfish server %q at %s", s.id, s.server.URL)
}

// GoString formats only the synthetic server ID and endpoint.
func (s *Server) GoString() string {
	return s.String()
}

// Requests returns detached sanitized observations.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count returns the number of matching method/path requests.
func (s *Server) Count(method string, path string) int {
	count := 0
	for _, request := range s.Requests() {
		if request.Method == method && request.Path == path {
			count++
		}
	}
	return count
}

// CountAuthentication returns the number of requests observed with an auth mode.
func (s *Server) CountAuthentication(authentication string) int {
	count := 0
	for _, request := range s.Requests() {
		if request.Authentication == authentication {
			count++
		}
	}
	return count
}

// SessionAlive reports whether the synthetic Session Service has an active session.
func (s *Server) SessionAlive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionAlive
}

func (s *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	authentication := s.authentication(request)
	s.record(request.Method, request.URL.Path, authentication)

	if request.URL.Path == serviceRootPath && request.Method == http.MethodGet {
		s.writeServiceRoot(writer)
		return
	}
	if request.URL.Path == sessionsPath || request.URL.Path == sessionsPath+"/1" {
		s.serveSession(writer, request)
		return
	}
	if !s.authorized(authentication) {
		writeRedfishError(writer, http.StatusUnauthorized)
		return
	}
	if handler, exists := s.routes[request.URL.Path]; exists {
		handler.ServeHTTP(writer, request)
		return
	}
	body, exists := s.resources[request.URL.Path]
	if !exists || request.Method != http.MethodGet {
		writeRedfishError(writer, http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(body)
}

func (s *Server) writeServiceRoot(writer http.ResponseWriter) {
	links := map[string]any{}
	if s.auth == AuthenticationSession {
		links["Sessions"] = map[string]string{"@odata.id": sessionsPath}
	}
	payload := map[string]any{
		"@odata.id":      serviceRootPath,
		"@odata.type":    "#ServiceRoot.v1_20_0.ServiceRoot",
		"Id":             "RootService",
		"Name":           "Synthetic Redfish Service " + s.id,
		"RedfishVersion": "1.20.0",
		"Links":          links,
		"Systems":        map[string]string{"@odata.id": "/redfish/v1/Systems"},
		"Chassis":        map[string]string{"@odata.id": "/redfish/v1/Chassis"},
		"Managers":       map[string]string{"@odata.id": "/redfish/v1/Managers"},
	}
	for name, value := range s.serviceRoot {
		payload[name] = value
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(payload)
}

func cloneObject(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func (s *Server) serveSession(writer http.ResponseWriter, request *http.Request) {
	if s.auth != AuthenticationSession {
		writeRedfishError(writer, http.StatusNotFound)
		return
	}
	if request.Method == http.MethodPost {
		var credentials struct {
			Username string `json:"UserName"`
			Password string `json:"Password"`
		}
		if err := json.NewDecoder(request.Body).Decode(&credentials); err != nil ||
			credentials.Username != s.username || credentials.Password != s.password {
			writeRedfishError(writer, http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.sessionAlive = true
		s.mu.Unlock()
		writer.Header().Set("X-Auth-Token", s.token)
		writer.Header().Set("Location", sessionsPath+"/1")
		writer.WriteHeader(http.StatusCreated)
		return
	}
	if request.Method == http.MethodDelete && request.URL.Path == sessionsPath+"/1" {
		if s.sessionDeleteStatus != 0 && s.sessionDeleteStatus != http.StatusNoContent {
			writeRedfishError(writer, s.sessionDeleteStatus)
			return
		}
		s.mu.Lock()
		s.sessionAlive = false
		s.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeRedfishError(writer, http.StatusMethodNotAllowed)
}

func (s *Server) authentication(request *http.Request) string {
	if request.Header.Get("X-Auth-Token") == s.token {
		return "session"
	}
	username, password, ok := request.BasicAuth()
	if ok && username == s.username && password == s.password {
		return "basic"
	}
	if ok || request.Header.Get("X-Auth-Token") != "" {
		return "invalid"
	}
	return "none"
}

func (s *Server) authorized(authentication string) bool {
	if s.auth == AuthenticationBasic {
		return authentication == "basic"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return authentication == "session" && s.sessionAlive
}

func (s *Server) record(method string, path string, authentication string) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method:         method,
		Path:           path,
		Authentication: authentication,
	})
	s.mu.Unlock()
}

func writeRedfishError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]string{
			"code":    fmt.Sprintf("Synthetic.%d", status),
			"message": "synthetic Redfish error",
		},
	})
}

//go:embed testdata/*.json
var fixtureFiles embed.FS

// Fixture loads a synthetic JSON fixture by filename.
func Fixture(t testing.TB, name string) json.RawMessage {
	t.Helper()
	contents, err := fixtureFiles.ReadFile("testdata/" + filepath.Base(name))
	if err != nil {
		t.Fatalf("read Redfish fixture %q: %v", name, err)
	}
	if !json.Valid(contents) {
		t.Fatalf("Redfish fixture %q is not valid JSON", name)
	}
	return append(json.RawMessage(nil), contents...)
}
