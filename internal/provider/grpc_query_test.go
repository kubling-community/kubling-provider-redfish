package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
)

func TestGRPCQueriesAllGeneratedBaseTablesAgainstOneDevice(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "inventory",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems":    json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
			"/redfish/v1/Systems/1":  testredfish.Fixture(t, "system-a.json"),
			"/redfish/v1/Chassis":    json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`),
			"/redfish/v1/Chassis/1":  testredfish.Fixture(t, "chassis-a.json"),
			"/redfish/v1/Managers":   json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1"}]}`),
			"/redfish/v1/Managers/1": testredfish.Fixture(t, "manager-a.json"),
		},
	})
	implementation := queryTestProvider(t, server)
	client := newGRPCTestClient(t, implementation)
	ctx := context.Background()
	opened, err := client.OpenConnection(ctx, &providerv1.OpenConnectionRequest{})
	if err != nil {
		t.Fatalf("OpenConnection() error = %v", err)
	}
	defer func() {
		_, _ = client.CloseConnection(ctx, &providerv1.CloseConnectionRequest{ConnectionId: opened.GetConnectionId()})
	}()

	wantIDs := map[string]string{
		"DEVICES":  "RootService",
		"SYSTEMS":  "system-a",
		"CHASSIS":  "chassis-a",
		"MANAGERS": "manager-a",
	}
	for _, table := range []string{"DEVICES", "SYSTEMS", "CHASSIS", "MANAGERS"} {
		t.Run(table, func(t *testing.T) {
			batchSize := uint32(1)
			stream, err := client.Query(ctx, &providerv1.QueryRequest{
				ConnectionId: opened.GetConnectionId(),
				Entity:       &providerv1.EntityReference{Name: table, Namespace: catalog.Namespace},
				Filter: grpcAnd(
					grpcEquality("namespace", "dc-a"),
					grpcEquality("device_id", "bmc-01"),
				),
				BatchSize:     &batchSize,
				AcceptOutcome: true,
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			var batches []*providerv1.TupleBatch
			var outcome *providerv1.QueryOutcome
			for {
				response, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Query.Recv() error = %v", err)
				}
				if response.GetBatch() != nil {
					batches = append(batches, response.GetBatch())
				}
				if response.GetOutcome() != nil {
					outcome = response.GetOutcome()
				}
			}
			if len(batches) != 1 || len(batches[0].GetTuples()) != 1 {
				t.Fatalf("batches = %v", batches)
			}
			if outcome == nil || outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
				len(outcome.GetWarnings()) != 0 {
				t.Fatalf("outcome = %v", outcome)
			}
			values := tupleValuesByField(t, batches[0])
			if values["namespace"].GetStringValue() != "dc-a" ||
				values["device_id"].GetStringValue() != "bmc-01" ||
				values["redfish_id"].GetStringValue() != wantIDs[table] ||
				values["resource_uri"].GetStringValue() == "" ||
				values["observed_at"].GetTimestampValue() == "" {
				t.Fatalf("projected %s values = %v", table, values)
			}
			if _, exists := values[catalog.IdentifierColumn]; exists {
				t.Fatalf("provider returned engine-generated %q", catalog.IdentifierColumn)
			}
		})
	}

	for _, path := range []string{
		"/redfish/v1/Systems",
		"/redfish/v1/Systems/1",
		"/redfish/v1/Chassis",
		"/redfish/v1/Chassis/1",
		"/redfish/v1/Managers",
		"/redfish/v1/Managers/1",
	} {
		if server.Count(http.MethodGet, path) != 1 {
			t.Fatalf("GET %s count = %d; requests = %v", path, server.Count(http.MethodGet, path), server.Requests())
		}
	}
}

func TestGRPCQueriesNamespaceRegistryWithoutContactingBMC(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "namespace-registry",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
	})
	implementation := queryTestProviderWithConfig(
		t,
		queryProviderConfig(),
		queryConfiguredDevice(t, "dc-b", "bmc-03", server),
		queryConfiguredDevice(t, "dc-a", "bmc-02", server),
		queryConfiguredDevice(t, "dc-a", "bmc-01", server),
	)
	client := newGRPCTestClient(t, implementation)
	ctx := context.Background()
	opened, err := client.OpenConnection(ctx, &providerv1.OpenConnectionRequest{})
	if err != nil {
		t.Fatalf("OpenConnection() error = %v", err)
	}
	defer func() {
		_, _ = client.CloseConnection(ctx, &providerv1.CloseConnectionRequest{ConnectionId: opened.GetConnectionId()})
	}()

	batchSize := uint32(1)
	stream, err := client.Query(ctx, &providerv1.QueryRequest{
		ConnectionId: opened.GetConnectionId(),
		Entity: &providerv1.EntityReference{
			Name:      "NAMESPACES",
			Namespace: catalog.Namespace,
		},
		BatchSize:     &batchSize,
		AcceptOutcome: true,
	})
	if err != nil {
		t.Fatalf("Query(NAMESPACES) error = %v", err)
	}
	type namespaceResult struct {
		name  string
		count int64
	}
	var rows []namespaceResult
	var outcome *providerv1.QueryOutcome
	for {
		response, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Query(NAMESPACES).Recv() error = %v", recvErr)
		}
		if response.GetOutcome() != nil {
			outcome = response.GetOutcome()
			continue
		}
		batch := response.GetBatch()
		values := tupleValuesByField(t, batch)
		rows = append(rows, namespaceResult{
			name:  values["namespace"].GetStringValue(),
			count: values["device_count"].GetLongValue(),
		})
	}
	if !reflect.DeepEqual(rows, []namespaceResult{{name: "dc-a", count: 2}, {name: "dc-b", count: 1}}) {
		t.Fatalf("NAMESPACES rows = %v", rows)
	}
	if outcome == nil || outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
		len(outcome.GetWarnings()) != 0 {
		t.Fatalf("NAMESPACES outcome = %v", outcome)
	}
	if len(server.Requests()) != 0 {
		t.Fatalf("NAMESPACES query contacted the BMC: %v", server.Requests())
	}
}

func TestGRPCQueriesGeneratedPowerGraphAgainstOneDevice(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "power-graph",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Chassis": json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`),
			"/redfish/v1/Chassis/1": json.RawMessage(`{
				"@odata.id":"/redfish/v1/Chassis/1",
				"EnvironmentMetrics":{"@odata.id":"/redfish/v1/Chassis/1/EnvironmentMetrics"}
			}`),
			"/redfish/v1/Chassis/1/EnvironmentMetrics": json.RawMessage(`{
				"@odata.id":"/redfish/v1/Chassis/1/EnvironmentMetrics",
				"@odata.type":"#EnvironmentMetrics.v1_7_0.EnvironmentMetrics",
				"Id":"EnvironmentMetrics","Name":"Environment metrics",
				"PowerWatts":{"Reading":321.5},"TemperatureCelsius":{"Reading":27.5}
			}`),
		},
	})
	implementation := queryTestProvider(t, server)
	client := newGRPCTestClient(t, implementation)
	ctx := context.Background()
	opened, err := client.OpenConnection(ctx, &providerv1.OpenConnectionRequest{})
	if err != nil {
		t.Fatalf("OpenConnection() error = %v", err)
	}
	defer func() {
		_, _ = client.CloseConnection(ctx, &providerv1.CloseConnectionRequest{ConnectionId: opened.GetConnectionId()})
	}()

	batchSize := uint32(1)
	stream, err := client.Query(ctx, &providerv1.QueryRequest{
		ConnectionId: opened.GetConnectionId(),
		Entity: &providerv1.EntityReference{
			Name:      "CHASSIS_ENVIRONMENT_METRICS",
			Namespace: catalog.Namespace,
		},
		Filter: grpcAnd(
			grpcEquality("namespace", "dc-a"),
			grpcEquality("device_id", "bmc-01"),
		),
		BatchSize:     &batchSize,
		AcceptOutcome: true,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	var batch *providerv1.TupleBatch
	var outcome *providerv1.QueryOutcome
	for {
		response, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Query.Recv() error = %v", recvErr)
		}
		if response.GetBatch() != nil {
			if batch != nil {
				t.Fatalf("unexpected second batch = %v", response.GetBatch())
			}
			batch = response.GetBatch()
		}
		if response.GetOutcome() != nil {
			outcome = response.GetOutcome()
		}
	}
	if batch == nil || len(batch.GetTuples()) != 1 {
		t.Fatalf("batch = %v", batch)
	}
	if outcome == nil || outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
		len(outcome.GetWarnings()) != 0 {
		t.Fatalf("outcome = %v", outcome)
	}
	values := tupleValuesByField(t, batch)
	if values["chassis_uri"].GetStringValue() != "/redfish/v1/Chassis/1" ||
		values["resource_uri"].GetStringValue() != "/redfish/v1/Chassis/1/EnvironmentMetrics" ||
		values["model_source"].GetStringValue() != "modern" ||
		values["power_watts"].GetDoubleValue() != 321.5 ||
		values["temperature_celsius"].GetDoubleValue() != 27.5 {
		t.Fatalf("projected power graph values = %v", values)
	}
}

func queryTestProvider(t *testing.T, server *testredfish.Server) *Provider {
	t.Helper()
	return queryTestProviderWithConfig(
		t,
		queryProviderConfig(),
		queryConfiguredDevice(t, "dc-a", "bmc-01", server),
	)
}

func queryConfiguredDevice(
	t *testing.T,
	namespace string,
	id string,
	server *testredfish.Server,
) device.Device {
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
		Namespace:      namespace,
		ID:             id,
		Endpoint:       server.Endpoint(),
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeBasic,
		TLS:            device.NewTLSConfig(server.CAFile(), "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	return configured
}

func queryTestProviderWithConfig(
	t *testing.T,
	config Config,
	devices ...device.Device,
) *Provider {
	t.Helper()
	registry, err := device.NewRegistry(devices)
	if err != nil {
		t.Fatalf("device.NewRegistry() error = %v", err)
	}
	implementation, err := New(registry, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return implementation
}

func queryProviderConfig() Config {
	return Config{
		PerDeviceTimeout:      2 * time.Second,
		TotalTimeout:          5 * time.Second,
		MaxRowsPerDevice:      100,
		MaxRowsPerQuery:       100,
		MaxPagesPerCollection: 10,
		MaxResponseBytes:      1024 * 1024,
		BatchRows:             2,
		Retry: redfish.RetryPolicy{
			MaxAttempts: 1,
			MinBackoff:  time.Millisecond,
			MaxBackoff:  time.Millisecond,
		},
	}
}

func tupleValuesByField(t *testing.T, batch *providerv1.TupleBatch) map[string]*kublingv1.Value {
	t.Helper()
	if len(batch.GetTuples()) != 1 || len(batch.GetFields()) != len(batch.GetTuples()[0].GetValues()) {
		t.Fatalf("invalid batch shape: %v", batch)
	}
	result := make(map[string]*kublingv1.Value, len(batch.GetFields()))
	for index, field := range batch.GetFields() {
		result[field.GetName()] = batch.GetTuples()[0].GetValues()[index]
	}
	return result
}

func grpcEquality(field string, value string) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Comparison{Comparison: &providerv1.ComparisonExpression{
		Operator: providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
		Left: &providerv1.Expression{Kind: &providerv1.Expression_Field{Field: &providerv1.FieldReference{
			Name: field,
		}}},
		Right: &providerv1.Expression{Kind: &providerv1.Expression_Literal{Literal: &providerv1.Literal{
			Value: &kublingv1.Value{Kind: &kublingv1.Value_StringValue{StringValue: value}},
		}}},
	}}}
}

func grpcAnd(operands ...*providerv1.Expression) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Logical{Logical: &providerv1.LogicalExpression{
		Operator: providerv1.LogicalOperator_LOGICAL_OPERATOR_AND,
		Operands: operands,
	}}}
}
