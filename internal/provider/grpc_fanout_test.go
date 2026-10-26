package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCRoutesOneDeviceAndFansOutAcrossMultipleBMCs(t *testing.T) {
	first := inventoryQueryServer(t, "first")
	second := inventoryQueryServer(t, "second")
	config := queryProviderConfig()
	config.AllowUnboundedFanout = true
	config.MaxDevicesPerQuery = 2
	config.MaxConcurrentDevices = 2
	implementation := queryTestProviderWithConfig(
		t,
		config,
		queryConfiguredDevice(t, "dc-a", "bmc-01", first),
		queryConfiguredDevice(t, "dc-a", "bmc-02", second),
	)
	client := newGRPCTestClient(t, implementation)
	connectionID := openQueryConnection(t, client)

	routed := systemsGRPCRequest(connectionID)
	routed.Filter = grpcAnd(
		grpcEquality("namespace", "dc-a"),
		grpcEquality("device_id", "bmc-01"),
	)
	routedRows, routedOutcome, err := receiveGRPCQuery(client, routed)
	if err != nil {
		t.Fatalf("routed query error = %v", err)
	}
	if len(routedRows) != 1 || routedRows[0]["device_id"].GetStringValue() != "bmc-01" ||
		routedOutcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE {
		t.Fatalf("routed result = rows %v, outcome %v", routedRows, routedOutcome)
	}
	if first.Count(http.MethodGet, "/redfish/v1/Systems") != 1 ||
		second.Count(http.MethodGet, "/redfish/v1/Systems") != 0 {
		t.Fatalf("routed requests: first=%v second=%v", first.Requests(), second.Requests())
	}

	rows, outcome, err := receiveGRPCQuery(client, systemsGRPCRequest(connectionID))
	if err != nil {
		t.Fatalf("fan-out query error = %v", err)
	}
	if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
		len(outcome.GetWarnings()) != 0 || len(rows) != 2 {
		t.Fatalf("fan-out result = rows %v, outcome %v", rows, outcome)
	}
	identities := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, exists := row[catalog.IdentifierColumn]; exists {
			t.Fatalf("provider returned engine-generated %q", catalog.IdentifierColumn)
		}
		if row["resource_uri"].GetStringValue() != "/redfish/v1/Systems/1" ||
			row["redfish_id"].GetStringValue() != "system-a" {
			t.Fatalf("fan-out row = %v", row)
		}
		identities = append(identities,
			row["namespace"].GetStringValue()+"/"+row["device_id"].GetStringValue()+row["resource_uri"].GetStringValue(),
		)
	}
	if !sameStringsUnordered(identities, []string{
		"dc-a/bmc-01/redfish/v1/Systems/1",
		"dc-a/bmc-02/redfish/v1/Systems/1",
	}) {
		t.Fatalf("global resource identities = %v", identities)
	}
	if first.Count(http.MethodGet, "/redfish/v1/Systems") != 2 ||
		second.Count(http.MethodGet, "/redfish/v1/Systems") != 1 {
		t.Fatalf("fan-out requests: first=%v second=%v", first.Requests(), second.Requests())
	}
}

func TestGRPCStrictFailureAndNegotiatedPartialOutcome(t *testing.T) {
	good := inventoryQueryServer(t, "good")
	badTwo := unavailableInventoryServer(t, "bad-two", "response-body-canary-two")
	badThree := unavailableInventoryServer(t, "bad-three", "response-body-canary-three")
	config := queryProviderConfig()
	config.AllowPartialResults = true
	config.AllowUnboundedFanout = true
	config.MaxDevicesPerQuery = 3
	config.MaxConcurrentDevices = 3
	implementation := queryTestProviderWithConfig(
		t,
		config,
		queryConfiguredDevice(t, "dc-a", "bmc-01", good),
		queryConfiguredDevice(t, "dc-a", "bmc-02", badTwo),
		queryConfiguredDevice(t, "dc-a", "bmc-03", badThree),
	)
	client := newGRPCTestClient(t, implementation)
	capabilities, err := client.GetCapabilities(context.Background(), &providerv1.GetCapabilitiesRequest{})
	if err != nil || !capabilities.GetQuery().GetPartialResults() {
		t.Fatalf("partial capability = (%v, %v)", capabilities.GetQuery(), err)
	}
	connectionID := openQueryConnection(t, client)

	strictRows, strictOutcome, strictErr := receiveGRPCQuery(client, systemsGRPCRequest(connectionID))
	if status.Code(strictErr) != codes.Unavailable || strictOutcome != nil {
		t.Fatalf("strict result = rows %v, outcome %v, error %v", strictRows, strictOutcome, strictErr)
	}

	partialRequest := systemsGRPCRequest(connectionID)
	partialRequest.AllowPartialResults = true
	rows, outcome, err := receiveGRPCQuery(client, partialRequest)
	if err != nil {
		t.Fatalf("partial query error = %v", err)
	}
	if len(rows) != 1 || rows[0]["device_id"].GetStringValue() != "bmc-01" ||
		outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL {
		t.Fatalf("partial result = rows %v, outcome %v", rows, outcome)
	}
	warnings := outcome.GetWarnings()
	if len(warnings) != 2 {
		t.Fatalf("partial warnings = %v", warnings)
	}
	wantTargets := []string{
		"namespace=dc-a&device_id=bmc-02",
		"namespace=dc-a&device_id=bmc-03",
	}
	for index, warning := range warnings {
		if warning.GetTarget() != wantTargets[index] ||
			warning.GetCode() != "REDFISH_TARGET_UNAVAILABLE" ||
			!warning.GetRetryable() ||
			warning.GetRole() != providerv1.QueryDiagnosticRole_QUERY_DIAGNOSTIC_ROLE_PARTIAL_RESULT_CAUSE ||
			strings.Contains(warning.GetMessage(), "response-body-canary") {
			t.Fatalf("warning[%d] = %v", index, warning)
		}
	}
}

func inventoryQueryServer(t *testing.T, id string) *testredfish.Server {
	t.Helper()
	resource := testredfish.Fixture(t, "system-a.json")
	if id == "second" {
		resource = json.RawMessage(`{
			"@odata.id":"/redfish/v1/Systems/1",
			"@odata.type":"#ComputerSystem.v1_29_0.ComputerSystem",
			"Id":"system-a",
			"Name":"Synthetic System From Another Firmware",
			"Oem":{"SyntheticVendor":{"Generation":2}}
		}`)
	}
	return testredfish.NewServer(t, testredfish.Options{
		ID:             id,
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems":   json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
			"/redfish/v1/Systems/1": resource,
		},
	})
}

func unavailableInventoryServer(t *testing.T, id string, canary string) *testredfish.Server {
	t.Helper()
	return testredfish.NewServer(t, testredfish.Options{
		ID:             id,
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Routes: map[string]http.Handler{
			"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(writer, `{"error":{"code":"Synthetic.Unavailable","message":"`+canary+`"}}`)
			}),
		},
	})
}

func openQueryConnection(t *testing.T, client providerv1.ProviderServiceClient) string {
	t.Helper()
	opened, err := client.OpenConnection(context.Background(), &providerv1.OpenConnectionRequest{})
	if err != nil {
		t.Fatalf("OpenConnection() error = %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.CloseConnection(context.Background(), &providerv1.CloseConnectionRequest{
			ConnectionId: opened.GetConnectionId(),
		})
	})
	return opened.GetConnectionId()
}

func systemsGRPCRequest(connectionID string) *providerv1.QueryRequest {
	batchSize := uint32(1)
	return &providerv1.QueryRequest{
		ConnectionId:  connectionID,
		Entity:        &providerv1.EntityReference{Name: "SYSTEMS", Namespace: catalog.Namespace},
		BatchSize:     &batchSize,
		AcceptOutcome: true,
	}
}

func receiveGRPCQuery(
	client providerv1.ProviderServiceClient,
	request *providerv1.QueryRequest,
) ([]map[string]*kublingv1.Value, *providerv1.QueryOutcome, error) {
	stream, err := client.Query(context.Background(), request)
	if err != nil {
		return nil, nil, err
	}
	var rows []map[string]*kublingv1.Value
	var outcome *providerv1.QueryOutcome
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return rows, outcome, nil
		}
		if err != nil {
			return rows, outcome, err
		}
		if response.GetOutcome() != nil {
			outcome = response.GetOutcome()
			continue
		}
		batch := response.GetBatch()
		for _, tuple := range batch.GetTuples() {
			row := make(map[string]*kublingv1.Value, len(batch.GetFields()))
			for index, field := range batch.GetFields() {
				row[field.GetName()] = tuple.GetValues()[index]
			}
			rows = append(rows, row)
		}
	}
}

func sameStringsUnordered(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	remaining := append([]string(nil), want...)
	for _, value := range got {
		found := false
		for index, candidate := range remaining {
			if value == candidate {
				remaining = append(remaining[:index], remaining[index+1:]...)
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(remaining) == 0
}
