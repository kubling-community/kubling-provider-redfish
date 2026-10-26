package query

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExecutorMapsOperationalHTTPFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		grpcCode   codes.Code
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, grpcCode: codes.Unauthenticated},
		{name: "forbidden", statusCode: http.StatusForbidden, grpcCode: codes.PermissionDenied},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, grpcCode: codes.ResourceExhausted},
		{name: "server failure", statusCode: http.StatusInternalServerError, grpcCode: codes.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testredfish.NewServer(t, testredfish.Options{
				ID:             "status-" + test.name,
				Username:       "operator",
				Password:       "password",
				Authentication: testredfish.AuthenticationBasic,
				Routes: map[string]http.Handler{
					"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
						writer.Header().Set("Content-Type", "application/json")
						writer.WriteHeader(test.statusCode)
						_, _ = writer.Write([]byte(`{"error":{"code":"Synthetic.Failure","message":"body-canary"}}`))
					}),
				},
			})
			executor := queryTestExecutor(t, Config{})
			stream, err := executor.Query(
				context.Background(),
				querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
				queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01")),
			)
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if _, err := stream.Next(context.Background()); status.Code(err) != test.grpcCode {
				t.Fatalf("Next() code = %v, want %v; error = %v", status.Code(err), test.grpcCode, err)
			}
			_ = stream.Close()
		})
	}
}

func TestSlowSessionTargetTimesOutAndDeletesSession(t *testing.T) {
	started := make(chan struct{}, 1)
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "slow-session",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationSession,
		Routes: map[string]http.Handler{
			"/redfish/v1/Systems": http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				<-request.Context().Done()
			}),
		},
	})
	executor := hardeningExecutor(t, Config{
		PerDeviceTimeout: 40 * time.Millisecond,
		TotalTimeout:     time.Second,
	})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, sessionQueryDevice(t, server)),
		queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01")),
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if _, err := stream.Next(context.Background()); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Next() code = %v, want DeadlineExceeded; error = %v", status.Code(err), err)
	}
	select {
	case <-started:
	default:
		t.Fatal("slow Redfish route was not reached")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if server.SessionAlive() || server.Count(http.MethodDelete, "/redfish/v1/SessionService/Sessions/1") != 1 {
		t.Fatalf("timed-out query did not release its session: %v", server.Requests())
	}
}

func TestSessionDeletionFailureFailsQueryInsteadOfTruncatingSilently(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:                  "failed-session-delete",
		Username:            "operator",
		Password:            "password",
		Authentication:      testredfish.AuthenticationSession,
		SessionDeleteStatus: http.StatusServiceUnavailable,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems": json.RawMessage(`{"Members":[]}`),
		},
	})
	executor := hardeningExecutor(t, Config{})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, sessionQueryDevice(t, server)),
		queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01")),
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if _, err := stream.Next(context.Background()); status.Code(err) != codes.Unavailable {
		t.Fatalf("Next() code = %v, want Unavailable; error = %v", status.Code(err), err)
	}
	_ = stream.Close()
	if !server.SessionAlive() || server.Count(http.MethodDelete, "/redfish/v1/SessionService/Sessions/1") != 1 {
		t.Fatalf("session deletion failure was hidden: %v", server.Requests())
	}
}

func hardeningExecutor(t *testing.T, config Config) *Executor {
	t.Helper()
	if config.PerDeviceTimeout == 0 {
		config.PerDeviceTimeout = time.Second
	}
	if config.TotalTimeout == 0 {
		config.TotalTimeout = 2 * time.Second
	}
	config.Retry = redfish.RetryPolicy{
		MaxAttempts: 1,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	}
	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func sessionQueryDevice(t *testing.T, server *testredfish.Server) device.Device {
	t.Helper()
	username, err := secret.NewValue("operator")
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	password, err := secret.NewValue("password")
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configured, err := device.New(device.Definition{
		Namespace:      "dc-a",
		ID:             "bmc-01",
		Endpoint:       server.Endpoint(),
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeSession,
		TLS:            device.NewTLSConfig(server.CAFile(), "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	return configured
}
