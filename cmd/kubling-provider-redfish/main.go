package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/config"
	redfishprovider "github.com/kubling-community/kubling-provider-redfish/internal/provider"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	listenAddress := flag.String(
		"listen",
		"",
		"override the provider gRPC listen address from configuration",
	)
	configPath := flag.String(
		"config",
		os.Getenv("KUBLING_REDFISH_CONFIG"),
		"path to the Redfish provider YAML configuration",
	)
	flag.Parse()

	if err := run(*listenAddress, *configPath); err != nil {
		slog.Error("Redfish provider stopped", "error", err)
		os.Exit(1)
	}
}

func run(listenAddress string, configPath string) error {
	if strings.TrimSpace(configPath) == "" {
		return errors.New("Redfish provider config path is required")
	}
	configuration, err := config.Load(configPath)
	if err != nil {
		return err
	}
	execution := configuration.Execution()
	implementation, err := redfishprovider.New(configuration.Registry(), redfishprovider.Config{
		AllowPartialResults:   execution.AllowPartialResults,
		AllowUnboundedFanout:  execution.AllowUnboundedFanout,
		MaxDevicesPerQuery:    execution.MaxDevicesPerQuery,
		MaxConcurrentDevices:  execution.MaxConcurrentDevices,
		PerDeviceTimeout:      execution.PerDeviceTimeout,
		TotalTimeout:          execution.TotalTimeout,
		MaxRowsPerDevice:      execution.MaxRowsPerDevice,
		MaxRowsPerQuery:       execution.MaxRowsPerQuery,
		MaxPagesPerCollection: execution.MaxPagesPerCollection,
		MaxResponseBytes:      execution.MaxResponseBytes,
		BatchRows:             execution.BatchRows,
		Retry: redfish.RetryPolicy{
			MaxAttempts: execution.Retry.MaxAttempts,
			MinBackoff:  execution.Retry.MinBackoff,
			MaxBackoff:  execution.Retry.MaxBackoff,
		},
		Logger: slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("create Redfish provider: %w", err)
	}
	for _, warning := range configuration.Warnings() {
		slog.Warn("Redfish provider configuration warning", "warning", warning)
	}
	if strings.TrimSpace(listenAddress) == "" {
		listenAddress = configuration.ListenAddress()
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()

	service := providersdk.NewServer(implementation)
	grpcServer := grpc.NewServer()
	providerv1.RegisterProviderServiceServer(grpcServer, service)
	reflection.Register(grpcServer)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	slog.Info(
		"Redfish provider listening",
		"address",
		listener.Addr().String(),
		"config",
		configPath,
		"devices",
		configuration.Registry().Len(),
		"partial_results",
		execution.AllowPartialResults,
		"unbounded_fanout",
		execution.AllowUnboundedFanout,
		"max_devices_per_query",
		execution.MaxDevicesPerQuery,
		"max_concurrent_devices",
		execution.MaxConcurrentDevices,
		"max_rows_per_device",
		execution.MaxRowsPerDevice,
		"max_rows_per_query",
		execution.MaxRowsPerQuery,
	)

	serveErr := serveGRPC(
		ctx,
		grpcServer,
		listener,
		shutdownGracePeriod(execution.TotalTimeout, execution.PerDeviceTimeout),
	)
	closeErr := service.Close(context.Background())

	return errors.Join(serveErr, closeErr)
}

func serveGRPC(
	ctx context.Context,
	server *grpc.Server,
	listener net.Listener,
	gracePeriod time.Duration,
) error {
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	select {
	case serveErr := <-serveDone:
		server.Stop()
		return normalizeServeError(serveErr)
	case <-ctx.Done():
	}

	slog.Info("Redfish provider shutdown requested", "grace_period", gracePeriod)
	gracefulDone := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(gracefulDone)
	}()
	timer := time.NewTimer(gracePeriod)
	select {
	case <-gracefulDone:
		timer.Stop()
	case <-timer.C:
		slog.Warn("Redfish provider graceful shutdown timed out", "grace_period", gracePeriod)
		server.Stop()
		<-gracefulDone
	}
	return normalizeServeError(<-serveDone)
}

func normalizeServeError(err error) error {
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func shutdownGracePeriod(totalTimeout time.Duration, perDeviceTimeout time.Duration) time.Duration {
	const maximumDuration = time.Duration(1<<63 - 1)
	if totalTimeout > maximumDuration-perDeviceTimeout {
		return maximumDuration
	}
	return totalTimeout + perDeviceTimeout
}
