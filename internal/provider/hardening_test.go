package provider

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStructuredQueryLogsAndGRPCErrorExcludeSensitiveValues(t *testing.T) {
	const (
		usernameCanary = "USERNAME-LOG-CANARY"
		passwordCanary = "PASSWORD-LOG-CANARY"
		queryCanary    = "QUERY-LOG-CANARY"
		bodyCanary     = "BODY-LOG-CANARY"
		tokenCanary    = "TOKEN-LOG-CANARY"
	)
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "logging-redaction",
		Username:       usernameCanary,
		Password:       passwordCanary,
		Authentication: testredfish.AuthenticationBasic,
		Routes: map[string]http.Handler{
			"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("X-Auth-Token", tokenCanary)
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = writer.Write([]byte(`{"error":{"code":"Synthetic.Secret","message":"` + bodyCanary + `"}}`))
			}),
		},
	})
	username, err := secret.NewValue(usernameCanary)
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	password, err := secret.NewValue(passwordCanary)
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configured, err := device.New(device.Definition{
		Namespace:      "dc-a",
		ID:             "bmc-01",
		Endpoint:       server.Endpoint(),
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeBasic,
		TLS:            device.NewTLSConfig(server.CAFile(), "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}

	var output bytes.Buffer
	config := queryProviderConfig()
	config.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	implementation := queryTestProviderWithConfig(t, config, configured)
	client := newGRPCTestClient(t, implementation)
	connectionID := openQueryConnection(t, client)
	request := systemsGRPCRequest(connectionID)
	request.Filter = grpcEquality("device_id", "bmc-01")
	request.Projections = []*providerv1.Projection{{
		OutputName: queryCanary,
		Expression: &providerv1.Expression{Kind: &providerv1.Expression_Field{
			Field: &providerv1.FieldReference{Name: "resource_uri"},
		}},
	}}

	_, _, queryErr := receiveGRPCQuery(client, request)
	if status.Code(queryErr) != codes.Unavailable {
		t.Fatalf("query code = %v, want Unavailable; error = %v", status.Code(queryErr), queryErr)
	}
	serializedError := fmt.Sprintf("%v %#v", queryErr, queryErr)
	logged := output.String()
	for _, canary := range []string{usernameCanary, passwordCanary, queryCanary, bodyCanary, tokenCanary} {
		if strings.Contains(serializedError, canary) {
			t.Fatalf("gRPC error contains %q: %s", canary, serializedError)
		}
		if strings.Contains(logged, canary) {
			t.Fatalf("structured log contains %q: %s", canary, logged)
		}
	}
	for _, expected := range []string{
		`"msg":"Redfish query started"`,
		`"msg":"Redfish query target failed"`,
		`"msg":"Redfish query finished"`,
		`"query_id":"redfish-1"`,
		`"namespace":"dc-a"`,
		`"device_id":"bmc-01"`,
		`"code":"REDFISH_TARGET_UNAVAILABLE"`,
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("structured log does not contain %s: %s", expected, logged)
		}
	}
	if server.Count(http.MethodGet, "/redfish/v1/Systems") != 1 {
		t.Fatalf("unexpected Redfish requests: %v", server.Requests())
	}
}
