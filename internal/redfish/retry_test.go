package redfish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
)

func TestClientRetriesGETWithBackoffAndRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "retry",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Routes: map[string]http.Handler{
			"/redfish/v1/Retry": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				switch attempts.Add(1) {
				case 1:
					writer.Header().Set("Retry-After", "2")
					writeTestError(writer, http.StatusServiceUnavailable, "first failure")
				case 2:
					writeTestError(writer, http.StatusBadGateway, "second failure")
				default:
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, `{"Id":"recovered"}`)
				}
			}),
		},
	})

	var sleepMu sync.Mutex
	var sleeps []time.Duration
	client, err := newClient(
		context.Background(),
		testDevice(t, server.Endpoint(), server.CAFile(), false, device.AuthModeBasic, "operator", "password"),
		Options{
			RequestTimeout:   2 * time.Second,
			MaxResponseBytes: 1024 * 1024,
			Retry: RetryPolicy{
				MaxAttempts: 3,
				MinBackoff:  10 * time.Millisecond,
				MaxBackoff:  3 * time.Second,
			},
		},
		clientDependencies{
			sleep: func(_ context.Context, duration time.Duration) error {
				sleepMu.Lock()
				sleeps = append(sleeps, duration)
				sleepMu.Unlock()
				return nil
			},
			now: func() time.Time { return time.Unix(1_800_000_000, 0) },
		},
	)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	defer client.Close()

	document, err := client.Get(context.Background(), "/redfish/v1/Retry")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var payload struct {
		ID string `json:"Id"`
	}
	if err := document.Decode(&payload); err != nil || payload.ID != "recovered" {
		t.Fatalf("Decode() = (%q, %v)", payload.ID, err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleeps) != 2 || sleeps[0] != 2*time.Second || sleeps[1] != 20*time.Millisecond {
		t.Fatalf("retry sleeps = %v", sleeps)
	}
}

func TestRetryTransportNeverRetriesPOST(t *testing.T) {
	var calls atomic.Int32
	transport := &retryTransport{
		next: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{}}`)),
			}, nil
		}),
		policy: RetryPolicy{MaxAttempts: 3, MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		sleep:  func(context.Context, time.Duration) error { return nil },
		now:    time.Now,
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://bmc.example.invalid/session", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	defer response.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("POST calls = %d, want 1", calls.Load())
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	delay, ok := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now)
	if !ok || delay != 5*time.Second {
		t.Fatalf("parseRetryAfter() = (%v, %v)", delay, ok)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func writeTestError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]string{
			"code":    "Synthetic.Transient",
			"message": message,
		},
	})
}
