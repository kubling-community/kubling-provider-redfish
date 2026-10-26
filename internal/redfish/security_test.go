package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
)

func TestRedirectsStayOnTheOriginalHTTPSOrigin(t *testing.T) {
	var crossOriginHits atomic.Int32
	crossOrigin := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		crossOriginHits.Add(1)
	}))
	defer crossOrigin.Close()

	source := testredfish.NewServer(t, testredfish.Options{
		ID:             "redirect",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Target": json.RawMessage(`{"Id":"target"}`),
		},
		Routes: map[string]http.Handler{
			"/redfish/v1/SameOrigin": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Location", "/redfish/v1/Target?opaque=same-origin")
				writer.WriteHeader(http.StatusFound)
			}),
			"/redfish/v1/CrossOrigin": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Location", crossOrigin.URL+"/capture?token=REDIRECT-QUERY-CANARY")
				writer.WriteHeader(http.StatusFound)
			}),
			"/redfish/v1/Downgrade": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Location", "http://127.0.0.1/redfish/v1/Target")
				writer.WriteHeader(http.StatusFound)
			}),
		},
	})
	client, err := NewClient(
		context.Background(),
		testDevice(t, source.Endpoint(), source.CAFile(), false, device.AuthModeBasic, "operator", "password"),
		oneAttemptOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	document, err := client.Get(context.Background(), "/redfish/v1/SameOrigin")
	if err != nil {
		t.Fatalf("same-origin Get() error = %v", err)
	}
	if document.URI() != "/redfish/v1/Target?opaque=same-origin" {
		t.Fatalf("redirected Document.URI() = %q", document.URI())
	}
	for _, path := range []string{"/redfish/v1/CrossOrigin", "/redfish/v1/Downgrade"} {
		_, err := client.Get(context.Background(), path)
		assertErrorKind(t, err, ErrorKindRedirect)
		if strings.Contains(fmt.Sprintf("%v %#v", err, err), "REDIRECT-QUERY-CANARY") {
			t.Fatalf("redirect error exposes query: %v", err)
		}
	}
	if crossOriginHits.Load() != 0 {
		t.Fatalf("cross-origin server received %d requests", crossOriginHits.Load())
	}
}

func TestErrorsRedactCredentialsQueriesTokensAndBodies(t *testing.T) {
	const (
		usernameCanary = "USERNAME-CANARY"
		passwordCanary = "PASSWORD-CANARY"
		queryCanary    = "QUERY-CANARY"
		bodyCanary     = "BODY-CANARY"
		tokenCanary    = "TOKEN-CANARY"
	)
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "redaction",
		Username:       usernameCanary,
		Password:       passwordCanary,
		Authentication: testredfish.AuthenticationBasic,
		Routes: map[string]http.Handler{
			"/redfish/v1/Failure": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("X-Auth-Token", tokenCanary)
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(writer, `{"error":{"code":"Synthetic.Secret","message":"`+bodyCanary+`"}}`)
			}),
		},
	})
	client, err := NewClient(
		context.Background(),
		testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeBasic, usernameCanary, passwordCanary),
		oneAttemptOptions(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	_, err = client.Get(context.Background(), "/redfish/v1/Failure?access_token="+queryCanary)
	redfishError := assertErrorKind(t, err, ErrorKindHTTP)
	if redfishError.StatusCode() != http.StatusInternalServerError || !redfishError.Retryable() {
		t.Fatalf("error metadata = status %d retryable %v", redfishError.StatusCode(), redfishError.Retryable())
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		formatted := fmt.Sprintf("%v %+v %#v", current, current, current)
		for _, canary := range []string{usernameCanary, passwordCanary, queryCanary, bodyCanary, tokenCanary} {
			if strings.Contains(formatted, canary) {
				t.Fatalf("error chain exposes %q: %s", canary, formatted)
			}
		}
	}
	clientFormatted := fmt.Sprintf("%v %#v", client, client)
	if strings.Contains(clientFormatted, usernameCanary) || strings.Contains(clientFormatted, passwordCanary) {
		t.Fatalf("client formatting exposes credentials: %q", clientFormatted)
	}
}

func TestResponseLimitAndMalformedJSONAreClassified(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "responses",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Routes: map[string]http.Handler{
			"/redfish/v1/Large": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"Data":"`+strings.Repeat("x", 2048)+`"}`)
			}),
			"/redfish/v1/Malformed": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, `{"Id":`)
			}),
		},
	})
	options := oneAttemptOptions()
	options.MaxResponseBytes = 1024
	client, err := NewClient(
		context.Background(),
		testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeBasic, "operator", "password"),
		options,
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	_, err = client.Get(context.Background(), "/redfish/v1/Large")
	assertErrorKind(t, err, ErrorKindResponseLimit)
	_, err = client.Get(context.Background(), "/redfish/v1/Malformed")
	assertErrorKind(t, err, ErrorKindInvalidResponse)
}

func TestHTTPStatusClassification(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		kind       ErrorKind
		retryable  bool
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, kind: ErrorKindAuthentication},
		{name: "forbidden", statusCode: http.StatusForbidden, kind: ErrorKindAuthentication},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, kind: ErrorKindHTTP, retryable: true},
		{name: "server failure", statusCode: http.StatusInternalServerError, kind: ErrorKindHTTP, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testredfish.NewServer(t, testredfish.Options{
				ID:             strings.ReplaceAll(test.name, " ", "-"),
				Username:       "operator",
				Password:       "password",
				Authentication: testredfish.AuthenticationBasic,
				Routes: map[string]http.Handler{
					"/redfish/v1/Failure": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
						writeTestError(writer, test.statusCode, "source-body-canary")
					}),
				},
			})
			client, err := NewClient(
				context.Background(),
				testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeBasic, "operator", "password"),
				oneAttemptOptions(),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			defer client.Close()

			_, err = client.Get(context.Background(), "/redfish/v1/Failure")
			redfishError := assertErrorKind(t, err, test.kind)
			if redfishError.StatusCode() != test.statusCode || redfishError.Retryable() != test.retryable {
				t.Fatalf("error metadata = status %d retryable %v", redfishError.StatusCode(), redfishError.Retryable())
			}
			if strings.Contains(fmt.Sprintf("%v %#v", err, err), "source-body-canary") {
				t.Fatalf("error exposes response body: %v", err)
			}
		})
	}
}

func TestLimitedResponseReaderStopsAfterOneByteProbe(t *testing.T) {
	source := &countingReader{remaining: 64 * 1024 * 1024}
	reader := &limitedReadCloser{
		body:      io.NopCloser(source),
		remaining: 1024,
	}
	body, err := io.ReadAll(reader)
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("ReadAll() error = %v, want response limit", err)
	}
	if len(body) != 1024 || source.read != 1025 {
		t.Fatalf("bounded read = body %d bytes, source %d bytes", len(body), source.read)
	}
}

type countingReader struct {
	remaining int64
	read      int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := int64(len(buffer))
	if count > r.remaining {
		count = r.remaining
	}
	for index := int64(0); index < count; index++ {
		buffer[index] = 'x'
	}
	r.remaining -= count
	r.read += count
	return int(count), nil
}
