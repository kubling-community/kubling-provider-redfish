// Package provider implements the Kubling Redfish provider lifecycle.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	redfishquery "github.com/kubling-community/kubling-provider-redfish/internal/query"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/status"
)

// Provider exposes Redfish infrastructure through the Kubling provider API.
type Provider struct {
	devices       *device.Registry
	config        Config
	queryExecutor *redfishquery.Executor
}

// Config controls provider execution and observability.
type Config struct {
	AllowPartialResults   bool
	AllowUnboundedFanout  bool
	MaxDevicesPerQuery    int
	MaxConcurrentDevices  int
	PerDeviceTimeout      time.Duration
	TotalTimeout          time.Duration
	MaxRowsPerDevice      int
	MaxRowsPerQuery       int
	MaxPagesPerCollection int
	MaxResponseBytes      int64
	BatchRows             int
	Retry                 redfish.RetryPolicy
	// Logger receives safe structural query events. Query values and secrets are omitted.
	Logger *slog.Logger
}

// String formats provider cardinality without device credentials.
func (p *Provider) String() string {
	if p == nil {
		return "Redfish provider (nil)"
	}
	return fmt.Sprintf("Redfish provider (%d devices)", p.devices.Len())
}

// GoString formats provider cardinality without device credentials.
func (p *Provider) GoString() string {
	return p.String()
}

// New creates a Redfish provider process.
func New(devices *device.Registry, config Config) (*Provider, error) {
	if devices == nil || devices.Len() == 0 {
		return nil, errors.New("Redfish device registry must not be empty")
	}
	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("validate generated Redfish catalog: %w", err)
	}
	executor, err := redfishquery.NewExecutor(redfishquery.Config{
		AllowPartialResults:   config.AllowPartialResults,
		AllowUnboundedFanout:  config.AllowUnboundedFanout,
		MaxDevicesPerQuery:    config.MaxDevicesPerQuery,
		MaxConcurrentDevices:  config.MaxConcurrentDevices,
		PerDeviceTimeout:      config.PerDeviceTimeout,
		TotalTimeout:          config.TotalTimeout,
		MaxRowsPerDevice:      config.MaxRowsPerDevice,
		MaxRowsPerQuery:       config.MaxRowsPerQuery,
		MaxPagesPerCollection: config.MaxPagesPerCollection,
		MaxResponseBytes:      config.MaxResponseBytes,
		BatchRows:             config.BatchRows,
		Retry:                 config.Retry,
		Logger:                config.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Redfish query executor: %w", err)
	}
	return &Provider{devices: devices, config: config, queryExecutor: executor}, nil
}

// Health reports process health independently from individual Redfish devices.
func (p *Provider) Health(ctx context.Context) (*providerv1.HealthResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return &providerv1.HealthResponse{
		Healthy: true,
		Message: fmt.Sprintf(
			"Redfish provider process is ready with %d configured device(s)",
			p.devices.Len(),
		),
	}, nil
}

// Open creates a logical connection to the provider.
func (p *Provider) Open(ctx context.Context) (providersdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return &Connection{devices: p.devices.Snapshot(), queryExecutor: p.queryExecutor}, nil
}

var _ providersdk.Provider = (*Provider)(nil)
