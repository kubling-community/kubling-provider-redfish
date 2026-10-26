package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestRunRequiresConfiguration(t *testing.T) {
	if err := run("", ""); err == nil || !strings.Contains(err.Error(), "config path is required") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunValidatesConfigurationBeforeOpeningListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("unknownField: true\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	err := run("not-a-valid-listen-address", path)
	if err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("run() error = %v, want configuration validation error", err)
	}
	if strings.Contains(err.Error(), "missing port") {
		t.Fatalf("run() opened listener before validating configuration: %v", err)
	}
}

func TestServeGRPCBoundsGracefulShutdown(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	blocking := &blockingHealthService{started: make(chan struct{})}
	providerv1.RegisterProviderServiceServer(server, blocking)

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveGRPC(ctx, server, listener, 40*time.Millisecond)
	}()
	connection, err := grpc.NewClient(
		"passthrough:///redfish-shutdown-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		cancel()
		server.Stop()
		_ = listener.Close()
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		cancel()
		server.Stop()
		_ = listener.Close()
	})

	rpcDone := make(chan error, 1)
	go func() {
		_, err := providerv1.NewProviderServiceClient(connection).Health(
			context.Background(),
			&providerv1.HealthRequest{},
		)
		rpcDone <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking RPC did not start")
	}
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serveGRPC() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveGRPC() did not force a bounded shutdown")
	}
	select {
	case err := <-rpcDone:
		if err == nil {
			t.Fatal("forced shutdown left the blocking RPC successful")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking RPC was not released by forced shutdown")
	}
}

type blockingHealthService struct {
	providerv1.UnimplementedProviderServiceServer
	started chan struct{}
}

func (s *blockingHealthService) Health(
	ctx context.Context,
	_ *providerv1.HealthRequest,
) (*providerv1.HealthResponse, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
