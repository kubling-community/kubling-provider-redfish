package query

import (
	"errors"
	"log/slog"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
)

const (
	defaultPerDeviceTimeout = 15 * time.Second
	defaultTotalTimeout     = 45 * time.Second
	defaultMaxDevices       = 32
	defaultMaxConcurrent    = 8
	defaultMaxRows          = 10_000
	defaultMaxQueryRows     = 50_000
	defaultMaxPages         = 100
	defaultMaxResponseBytes = 8 * 1024 * 1024
	defaultBatchRows        = 128
)

// Config bounds multi-device query execution.
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

func normalizeConfig(config Config) (Config, error) {
	if config.PerDeviceTimeout == 0 {
		config.PerDeviceTimeout = defaultPerDeviceTimeout
	}
	if config.TotalTimeout == 0 {
		config.TotalTimeout = defaultTotalTimeout
	}
	if config.MaxDevicesPerQuery == 0 {
		config.MaxDevicesPerQuery = defaultMaxDevices
	}
	if config.MaxConcurrentDevices == 0 {
		config.MaxConcurrentDevices = defaultMaxConcurrent
	}
	if config.MaxRowsPerDevice == 0 {
		config.MaxRowsPerDevice = defaultMaxRows
	}
	if config.MaxRowsPerQuery == 0 {
		config.MaxRowsPerQuery = defaultMaxQueryRows
	}
	if config.MaxPagesPerCollection == 0 {
		config.MaxPagesPerCollection = defaultMaxPages
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultMaxResponseBytes
	}
	if config.BatchRows == 0 {
		config.BatchRows = defaultBatchRows
	}
	defaultRetry := redfish.DefaultRetryPolicy()
	if config.Retry.MaxAttempts == 0 {
		config.Retry.MaxAttempts = defaultRetry.MaxAttempts
	}
	if config.Retry.MinBackoff == 0 {
		config.Retry.MinBackoff = defaultRetry.MinBackoff
	}
	if config.Retry.MaxBackoff == 0 {
		config.Retry.MaxBackoff = defaultRetry.MaxBackoff
	}
	if config.PerDeviceTimeout < 0 || config.TotalTimeout < 0 ||
		config.MaxDevicesPerQuery < 0 || config.MaxConcurrentDevices < 0 ||
		config.MaxRowsPerDevice < 0 || config.MaxRowsPerQuery < 0 ||
		config.MaxPagesPerCollection < 0 || config.MaxResponseBytes < 0 ||
		config.BatchRows < 0 {
		return Config{}, errors.New("Redfish query limits must be positive")
	}
	if config.Retry.MaxAttempts < 1 || config.Retry.MinBackoff < 0 ||
		config.Retry.MaxBackoff < config.Retry.MinBackoff {
		return Config{}, errors.New("invalid Redfish query retry policy")
	}
	return config, nil
}

func (c Config) batchSize(requested *uint32) int {
	if requested == nil || *requested == 0 || uint64(*requested) >= uint64(c.BatchRows) {
		return c.BatchRows
	}
	return int(*requested)
}

func (c Config) clientOptions() redfish.Options {
	return redfish.Options{
		RequestTimeout:   c.PerDeviceTimeout,
		MaxResponseBytes: c.MaxResponseBytes,
		Retry:            c.Retry,
	}
}
