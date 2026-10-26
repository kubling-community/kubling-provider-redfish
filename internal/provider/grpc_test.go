package provider

import (
	"context"
	"net"
	"testing"

	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCProviderBootstrapLifecycle(t *testing.T) {
	client := newGRPCTestClient(t, newTestProvider(t))
	ctx := context.Background()

	capabilities, err := client.GetCapabilities(ctx, &providerv1.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}
	if capabilities.GetTransactions().GetSupported() ||
		capabilities.GetQuery().GetPartialResults() ||
		capabilities.GetMutations().GetInsert() ||
		capabilities.GetMutations().GetUpdate() ||
		capabilities.GetMutations().GetDelete() {
		t.Fatalf("GetCapabilities() advertises unsupported behavior: %v", capabilities)
	}

	health, err := client.Health(ctx, &providerv1.HealthRequest{})
	if err != nil || !health.GetHealthy() {
		t.Fatalf("Health() = (%v, %v)", health, err)
	}

	schema, err := client.GetSchema(ctx, &providerv1.GetSchemaRequest{})
	if err != nil {
		t.Fatalf("GetSchema() error = %v", err)
	}
	if schema.GetSchemaDdl() != "" || len(schema.GetMetadata().GetTables()) != 22 ||
		schema.GetMetadata().GetProperties()["redfish.profile"] != "inventory-core" {
		t.Fatalf("GetSchema() = %v, want generated inventory-core metadata", schema)
	}

	opened, err := client.OpenConnection(ctx, &providerv1.OpenConnectionRequest{})
	if err != nil || opened.GetConnectionId() == "" {
		t.Fatalf("OpenConnection() = (%v, %v)", opened, err)
	}

	stream, err := client.Query(ctx, &providerv1.QueryRequest{ConnectionId: opened.GetConnectionId()})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Query() code = %v, want InvalidArgument", status.Code(err))
	}

	if _, err := client.BeginTransaction(ctx, &providerv1.BeginTransactionRequest{
		ConnectionId: opened.GetConnectionId(),
	}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("BeginTransaction() code = %v, want Unimplemented", status.Code(err))
	}

	if _, err := client.CloseConnection(ctx, &providerv1.CloseConnectionRequest{
		ConnectionId: opened.GetConnectionId(),
	}); err != nil {
		t.Fatalf("CloseConnection() error = %v", err)
	}
}

func newGRPCTestClient(t *testing.T, implementation *Provider) providerv1.ProviderServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	service := providersdk.NewServer(implementation)
	providerv1.RegisterProviderServiceServer(grpcServer, service)
	go func() { _ = grpcServer.Serve(listener) }()

	connection, err := grpc.NewClient(
		"passthrough:///redfish-provider-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		grpcServer.Stop()
		_ = listener.Close()
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		_ = service.Close(context.Background())
		grpcServer.Stop()
		_ = listener.Close()
	})
	return providerv1.NewProviderServiceClient(connection)
}
