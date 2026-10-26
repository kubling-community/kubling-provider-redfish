package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
)

func TestTwoTLSClientsUseIndependentDevicesAndAuthentication(t *testing.T) {
	serverA := testredfish.NewServer(t, testredfish.Options{
		ID:             "a",
		Username:       "operator-a",
		Password:       "password-a",
		Authentication: testredfish.AuthenticationSession,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems/1": testredfish.Fixture(t, "system-a.json"),
		},
	})
	serverB := testredfish.NewServer(t, testredfish.Options{
		ID:             "b",
		Username:       "operator-b",
		Password:       "password-b",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems/1": testredfish.Fixture(t, "system-b.json"),
		},
	})

	clientA, err := NewClient(
		context.Background(),
		testDevice(t, serverA.Endpoint(), serverA.CAFile(), false, device.AuthModeSession, "operator-a", "password-a"),
		testClientOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient(server A) error = %v", err)
	}
	defer clientA.Close()
	clientB, err := NewClient(
		context.Background(),
		testDevice(t, serverB.Endpoint()+"/", serverB.CAFile(), false, device.AuthModeBasic, "operator-b", "password-b"),
		testClientOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient(server B) error = %v", err)
	}
	defer clientB.Close()

	var rootA struct {
		Name string `json:"Name"`
	}
	if err := clientA.ServiceRoot().Decode(&rootA); err != nil || !strings.Contains(rootA.Name, " a") {
		t.Fatalf("clientA.ServiceRoot().Decode() = (%q, %v)", rootA.Name, err)
	}
	if clientA.ServiceRoot().ObservedAt().IsZero() {
		t.Fatal("clientA.ServiceRoot().ObservedAt() is zero")
	}
	observedAt := clientA.ServiceRoot().ObservedAt()
	rootBytes := clientA.ServiceRoot().Raw()
	rootBytes[0] = 'x'
	if !json.Valid(clientA.ServiceRoot().Raw()) || clientA.ServiceRoot().ObservedAt() != observedAt {
		t.Fatal("mutating ServiceRoot().Raw() changed the captured document")
	}

	documentA, err := clientA.Get(context.Background(), serverA.Endpoint()+"/redfish/v1/Systems/1")
	if err != nil {
		t.Fatalf("clientA.Get() error = %v", err)
	}
	documentB, err := clientB.Get(context.Background(), "Systems/1")
	if err != nil {
		t.Fatalf("clientB.Get() error = %v", err)
	}
	var systemA, systemB struct {
		ID string `json:"Id"`
	}
	if err := documentA.Decode(&systemA); err != nil {
		t.Fatalf("documentA.Decode() error = %v", err)
	}
	if err := documentB.Decode(&systemB); err != nil {
		t.Fatalf("documentB.Decode() error = %v", err)
	}
	if systemA.ID != "system-a" || systemB.ID != "system-b" {
		t.Fatalf("system IDs = (%q, %q)", systemA.ID, systemB.ID)
	}
	if serverA.Count(http.MethodPost, "/redfish/v1/SessionService/Sessions") != 1 ||
		serverA.CountAuthentication("session") < 2 {
		t.Fatalf("server A requests = %v", serverA.Requests())
	}
	if serverB.CountAuthentication("basic") < 2 ||
		serverB.Count(http.MethodPost, "/redfish/v1/SessionService/Sessions") != 0 {
		t.Fatalf("server B requests = %v", serverB.Requests())
	}

	if err := clientA.Close(); err != nil {
		t.Fatalf("clientA.Close() error = %v", err)
	}
	if serverA.Count(http.MethodDelete, "/redfish/v1/SessionService/Sessions/1") != 1 {
		t.Fatalf("session DELETE requests = %v", serverA.Requests())
	}
}

func TestClientCloseUsesABoundedFreshContextToDeleteSession(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "session-close-after-cancel",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationSession,
	})
	ctx, cancel := context.WithCancel(context.Background())
	client, err := NewClient(
		ctx,
		testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeSession, "operator", "password"),
		oneAttemptOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if !server.SessionAlive() {
		t.Fatal("session was not created")
	}

	cancel()
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if server.SessionAlive() || server.Count(http.MethodDelete, "/redfish/v1/SessionService/Sessions/1") != 1 {
		t.Fatalf("session was not deleted: %v", server.Requests())
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestClientCloseReportsSessionDeletionFailure(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:                  "session-close-failure",
		Username:            "operator",
		Password:            "password",
		Authentication:      testredfish.AuthenticationSession,
		SessionDeleteStatus: http.StatusServiceUnavailable,
	})
	client, err := NewClient(
		context.Background(),
		testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeSession, "operator", "password"),
		oneAttemptOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	closeErr := client.Close()
	redfishError := assertErrorKind(t, closeErr, ErrorKindHTTP)
	if redfishError.StatusCode() != http.StatusServiceUnavailable || !redfishError.Retryable() {
		t.Fatalf("Close() metadata = status %d retryable %v", redfishError.StatusCode(), redfishError.Retryable())
	}
	if secondErr := client.Close(); secondErr != closeErr {
		t.Fatalf("second Close() error = %v, want same error %v", secondErr, closeErr)
	}
	if !server.SessionAlive() || server.Count(http.MethodDelete, "/redfish/v1/SessionService/Sessions/1") != 1 {
		t.Fatalf("failed session deletion was not observable: %v", server.Requests())
	}
}

func TestTLSVerificationRequiresConfiguredCAUnlessExplicitlyInsecure(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "tls",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
	})
	otherServer := testredfish.NewServer(t, testredfish.Options{
		ID:             "other-ca",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
	})

	for name, caFile := range map[string]string{
		"system roots": "",
		"wrong CA":     otherServer.CAFile(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewClient(
				context.Background(),
				testDevice(t, server.Endpoint(), caFile, false, device.AuthModeBasic, "operator", "password"),
				oneAttemptOptions(),
			)
			assertErrorKind(t, err, ErrorKindTLS)
		})
	}

	insecureClient, err := NewClient(
		context.Background(),
		testDevice(t, server.Endpoint(), "", true, device.AuthModeBasic, "operator", "password"),
		oneAttemptOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient(insecure) error = %v", err)
	}
	defer insecureClient.Close()
}

func TestAutoAuthenticationFallsBackOnlyWhenSessionsAreUnsupported(t *testing.T) {
	t.Run("unsupported session service", func(t *testing.T) {
		server := testredfish.NewServer(t, testredfish.Options{
			ID:             "basic-only",
			Username:       "operator",
			Password:       "password",
			Authentication: testredfish.AuthenticationBasic,
			Resources: map[string]json.RawMessage{
				"/redfish/v1/Systems/1": testredfish.Fixture(t, "system-a.json"),
			},
		})
		client, err := NewClient(
			context.Background(),
			testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeAuto, "operator", "password"),
			testClientOptions(),
		)
		if err != nil {
			t.Fatalf("NewClient(auto) error = %v", err)
		}
		defer client.Close()
		if _, err := client.Get(context.Background(), "/redfish/v1/Systems/1"); err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if server.Count(http.MethodPost, "/redfish/v1/SessionService/Sessions") != 0 ||
			server.CountAuthentication("basic") == 0 {
			t.Fatalf("requests = %v", server.Requests())
		}
	})

	t.Run("invalid credentials", func(t *testing.T) {
		const passwordCanary = "WRONG-PASSWORD-CANARY"
		server := testredfish.NewServer(t, testredfish.Options{
			ID:             "session",
			Username:       "operator",
			Password:       "correct-password",
			Authentication: testredfish.AuthenticationSession,
		})
		_, err := NewClient(
			context.Background(),
			testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeAuto, "operator", passwordCanary),
			oneAttemptOptions(),
		)
		assertErrorKind(t, err, ErrorKindAuthentication)
		if server.Count(http.MethodPost, "/redfish/v1/SessionService/Sessions") != 1 ||
			server.CountAuthentication("basic") != 0 {
			t.Fatalf("auto mode fell back after invalid credentials: %v", server.Requests())
		}
		if strings.Contains(fmt.Sprintf("%v %#v", err, err), passwordCanary) {
			t.Fatalf("authentication error exposes password: %v", err)
		}
	})

	t.Run("explicit session mode", func(t *testing.T) {
		server := testredfish.NewServer(t, testredfish.Options{
			ID:             "no-session-service",
			Username:       "operator",
			Password:       "password",
			Authentication: testredfish.AuthenticationBasic,
		})
		_, err := NewClient(
			context.Background(),
			testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeSession, "operator", "password"),
			oneAttemptOptions(),
		)
		assertErrorKind(t, err, ErrorKindAuthentication)
		if server.CountAuthentication("basic") != 0 {
			t.Fatalf("explicit session mode used Basic auth: %v", server.Requests())
		}
	})
}

func TestExportedAdapterSurfaceContainsNoGofishTypes(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(Client{}),
		reflect.TypeOf(Document{}),
		reflect.TypeOf(Options{}),
		reflect.TypeOf(RetryPolicy{}),
		reflect.TypeOf(Error{}),
	}
	for _, current := range types {
		for fieldIndex := 0; fieldIndex < current.NumField(); fieldIndex++ {
			field := current.Field(fieldIndex)
			if field.IsExported() && strings.Contains(field.Type.String(), "gofish") {
				t.Fatalf("exported field %s.%s exposes %s", current.Name(), field.Name, field.Type)
			}
		}
		pointer := reflect.PointerTo(current)
		for methodIndex := 0; methodIndex < pointer.NumMethod(); methodIndex++ {
			method := pointer.Method(methodIndex)
			if strings.Contains(method.Type.String(), "gofish") {
				t.Fatalf("exported method %s.%s exposes %s", current.Name(), method.Name, method.Type)
			}
		}
	}
}

func testDevice(
	t *testing.T,
	endpoint string,
	caFile string,
	insecure bool,
	authMode device.AuthMode,
	username string,
	password string,
) device.Device {
	t.Helper()
	usernameValue, err := secret.NewValue(username)
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	passwordValue, err := secret.NewValue(password)
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configuredDevice, err := device.New(device.Definition{
		Namespace:      "test",
		ID:             "bmc-01",
		Endpoint:       endpoint,
		CredentialName: "test",
		Credential:     device.NewCredential(usernameValue, passwordValue),
		AuthMode:       authMode,
		TLS:            device.NewTLSConfig(caFile, "", insecure),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	return configuredDevice
}

func testClientOptions() Options {
	return Options{
		RequestTimeout:   2 * time.Second,
		MaxResponseBytes: 1024 * 1024,
		Retry: RetryPolicy{
			MaxAttempts: 2,
			MinBackoff:  time.Millisecond,
			MaxBackoff:  2 * time.Millisecond,
		},
	}
}

func oneAttemptOptions() Options {
	options := testClientOptions()
	options.Retry.MaxAttempts = 1
	return options
}

func assertErrorKind(t *testing.T, err error, kind ErrorKind) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want kind %q", kind)
	}
	var redfishError *Error
	if !errors.As(err, &redfishError) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if redfishError.Kind() != kind {
		t.Fatalf("error kind = %q, want %q; error = %v", redfishError.Kind(), kind, err)
	}
	return redfishError
}
