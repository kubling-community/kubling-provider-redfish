package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderExposesConservativeBootstrap(t *testing.T) {
	implementation := newTestProvider(t)

	capabilities, err := implementation.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if capabilities.GetTransactions().GetSupported() ||
		capabilities.GetQuery().GetPartialResults() ||
		capabilities.GetQuery().GetOrdering().GetSupported() ||
		capabilities.GetQuery().GetPagination().GetLimit() ||
		capabilities.GetQuery().GetPagination().GetOffset() ||
		capabilities.GetMutations().GetInsert() ||
		capabilities.GetMutations().GetUpdate() ||
		capabilities.GetMutations().GetDelete() {
		t.Fatalf("Capabilities() advertises unsupported behavior: %v", capabilities)
	}
	if expressions := capabilities.GetQuery().GetExpressions(); !reflect.DeepEqual(expressions.GetComparisonOperators(), []providerv1.ComparisonOperator{
		providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
	}) ||
		!reflect.DeepEqual(expressions.GetLogicalOperators(), []providerv1.LogicalOperator{
			providerv1.LogicalOperator_LOGICAL_OPERATOR_AND,
		}) ||
		len(expressions.GetNullPredicateOperators()) != 0 ||
		len(expressions.GetPatternOperators()) != 0 {
		t.Fatalf("Capabilities() advertises expressions: %v", expressions)
	}
	types := capabilities.GetValues().GetSupportedTypes()
	wantTypes := []kublingv1.ValueType{
		kublingv1.ValueType_VALUE_TYPE_STRING,
		kublingv1.ValueType_VALUE_TYPE_BOOLEAN,
		kublingv1.ValueType_VALUE_TYPE_LONG,
		kublingv1.ValueType_VALUE_TYPE_DOUBLE,
		kublingv1.ValueType_VALUE_TYPE_TIMESTAMP,
		kublingv1.ValueType_VALUE_TYPE_JSON,
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("Capabilities() values = %v", capabilities.GetValues())
	}
	for index, valueType := range types {
		if valueType.GetType() != wantTypes[index] ||
			valueType.GetInput() != (wantTypes[index] == kublingv1.ValueType_VALUE_TYPE_STRING) ||
			!valueType.GetOutput() {
			t.Fatalf("Capabilities() values = %v", capabilities.GetValues())
		}
	}

	health, err := implementation.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !health.GetHealthy() || health.GetMessage() != "Redfish provider process is ready with 1 configured device(s)" {
		t.Fatalf("Health() = %v", health)
	}

	connection, err := implementation.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if connection == nil {
		t.Fatal("Open() connection = nil")
	}
	redfishConnection, ok := connection.(*Connection)
	if !ok || redfishConnection.devices.Len() != 1 {
		t.Fatalf("Open() connection snapshot = %#v", connection)
	}
	formatted := fmt.Sprintf("%v %#v %v %#v", implementation, implementation, connection, connection)
	if strings.Contains(formatted, "test-password") || strings.Contains(formatted, "test-operator") {
		t.Fatalf("provider formatting exposes credentials: %q", formatted)
	}
}

func TestProviderHonorsCanceledContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	implementation := newTestProvider(t)

	if _, err := implementation.Capabilities(ctx); status.Code(err) != codes.Canceled {
		t.Fatalf("Capabilities(canceled) code = %v, want Canceled", status.Code(err))
	}
	if _, err := implementation.Health(ctx); status.Code(err) != codes.Canceled {
		t.Fatalf("Health(canceled) code = %v, want Canceled", status.Code(err))
	}
	if _, err := implementation.Metadata(ctx); status.Code(err) != codes.Canceled {
		t.Fatalf("Metadata(canceled) code = %v, want Canceled", status.Code(err))
	}
	if _, err := implementation.Open(ctx); status.Code(err) != codes.Canceled {
		t.Fatalf("Open(canceled) code = %v, want Canceled", status.Code(err))
	}
}

func TestProviderRejectsEmptyRegistry(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("New(nil) error = %v", err)
	}
}

func TestProviderMetadataReflectsFanoutPolicyWithoutContactingDevices(t *testing.T) {
	bounded := newTestProvider(t)
	metadata, err := bounded.Metadata(context.Background())
	if err != nil {
		t.Fatalf("bounded.Metadata() error = %v", err)
	}
	if len(metadata.GetTables()) != 22 {
		t.Fatalf("bounded tables = %d, want 22", len(metadata.GetTables()))
	}
	for _, table := range metadata.GetTables() {
		wantPatterns := 2
		if table.GetName() == "NAMESPACES" {
			wantPatterns = 0
		}
		if len(table.GetAccessPatterns()) != wantPatterns {
			t.Fatalf("bounded table %s access patterns = %v", table.GetName(), table.GetAccessPatterns())
		}
	}

	unbounded := newTestProviderWithConfig(t, Config{AllowUnboundedFanout: true})
	metadata, err = unbounded.Metadata(context.Background())
	if err != nil {
		t.Fatalf("unbounded.Metadata() error = %v", err)
	}
	for _, table := range metadata.GetTables() {
		if len(table.GetAccessPatterns()) != 0 {
			t.Fatalf("unbounded table %s access patterns = %v", table.GetName(), table.GetAccessPatterns())
		}
	}
}

func TestConnectionRejectsUnimplementedMutationsAndTransactions(t *testing.T) {
	connection := &Connection{}
	ctx := context.Background()

	if err := connection.Begin(ctx); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Begin() code = %v, want Unimplemented", status.Code(err))
	}
	if err := connection.Commit(ctx); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Commit() code = %v, want Unimplemented", status.Code(err))
	}
	if err := connection.Rollback(ctx); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Rollback() code = %v, want Unimplemented", status.Code(err))
	}
	if active, err := connection.InTransaction(ctx); active || status.Code(err) != codes.Unimplemented {
		t.Fatalf("InTransaction() = %v, %v; want false, Unimplemented", active, err)
	}
	if _, err := connection.Insert(ctx, &providerv1.InsertRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Insert() code = %v, want Unimplemented", status.Code(err))
	}
	if _, err := connection.Update(ctx, &providerv1.UpdateRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Update() code = %v, want Unimplemented", status.Code(err))
	}
	if _, err := connection.Delete(ctx, &providerv1.DeleteRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Delete() code = %v, want Unimplemented", status.Code(err))
	}
}

func TestConnectionRejectsInvalidQueryBeforeContactingDevice(t *testing.T) {
	implementation := newTestProvider(t)
	connection, err := implementation.Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer connection.Close(context.Background())
	if _, err := connection.Query(context.Background(), &providerv1.QueryRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Query(invalid) code = %v, want InvalidArgument; error = %v", status.Code(err), err)
	}
}

func TestConnectionCloseIsIdempotentAndRejectsFurtherWork(t *testing.T) {
	connection := &Connection{}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := connection.Query(context.Background(), &providerv1.QueryRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Query() after Close code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestConnectionHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := &Connection{}

	if _, err := connection.Query(ctx, &providerv1.QueryRequest{}); status.Code(err) != codes.Canceled {
		t.Fatalf("Query(canceled) code = %v, want Canceled", status.Code(err))
	}
}

func newTestProvider(t *testing.T) *Provider {
	return newTestProviderWithConfig(t, Config{})
}

func newTestProviderWithConfig(t *testing.T, providerConfig Config) *Provider {
	t.Helper()
	username, err := secret.NewValue("test-operator")
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	password, err := secret.NewValue("test-password")
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configuredDevice, err := device.New(device.Definition{
		Namespace:      "test",
		ID:             "bmc-01",
		Endpoint:       "https://bmc-01.example.invalid",
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeAuto,
		TLS:            device.NewTLSConfig("", "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	registry, err := device.NewRegistry([]device.Device{configuredDevice})
	if err != nil {
		t.Fatalf("device.NewRegistry() error = %v", err)
	}
	implementation, err := New(registry, providerConfig)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return implementation
}
