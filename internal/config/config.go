// Package config loads and validates the Redfish provider runtime configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddress       = ":50051"
	defaultMaxDevicesPerQuery  = 32
	defaultMaxConcurrent       = 8
	defaultPerDeviceTimeout    = 15 * time.Second
	defaultTotalTimeout        = 45 * time.Second
	defaultMaxRowsPerDevice    = 10_000
	defaultMaxRowsPerQuery     = 50_000
	defaultMaxPages            = 100
	defaultMaxResponseBytes    = 8 * 1024 * 1024
	defaultBatchRows           = 128
	defaultRetryMaxAttempts    = 3
	defaultRetryMinimumBackoff = 100 * time.Millisecond
	defaultRetryMaximumBackoff = 2 * time.Second
)

// ProfileInventoryCore is the initial Redfish inventory, power and thermal profile.
const ProfileInventoryCore = "inventory-core"

// RetryConfig bounds retry behavior for a single device request.
type RetryConfig struct {
	MaxAttempts int
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
}

// ExecutionConfig contains bounded fan-out and result limits.
type ExecutionConfig struct {
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
	Retry                 RetryConfig
}

// Config is an immutable validated runtime configuration.
type Config struct {
	listenAddress string
	profiles      []string
	registry      *device.Registry
	execution     ExecutionConfig
	warnings      []string
}

// ListenAddress returns the configured gRPC listen address.
func (c Config) ListenAddress() string {
	return c.listenAddress
}

// Profiles returns a detached profile list.
func (c Config) Profiles() []string {
	return append([]string(nil), c.profiles...)
}

// Registry returns the immutable device registry.
func (c Config) Registry() *device.Registry {
	return c.registry
}

// Execution returns detached execution limits.
func (c Config) Execution() ExecutionConfig {
	return c.execution
}

// Warnings returns detached non-fatal configuration warnings.
func (c Config) Warnings() []string {
	return append([]string(nil), c.warnings...)
}

// String formats only non-secret configuration metadata.
func (c Config) String() string {
	return fmt.Sprintf(
		"Redfish config (listen=%q profiles=%v devices=%d)",
		c.listenAddress,
		c.profiles,
		c.registry.Len(),
	)
}

// GoString formats only non-secret configuration metadata.
func (c Config) GoString() string {
	return c.String()
}

type fileConfig struct {
	Listen      string                    `yaml:"listen"`
	Profiles    []string                  `yaml:"profiles"`
	Credentials map[string]fileCredential `yaml:"credentials"`
	Devices     []fileDevice              `yaml:"devices"`
	Execution   fileExecution             `yaml:"execution"`
}

type fileCredential struct {
	Username secret.Reference `yaml:"username"`
	Password secret.Reference `yaml:"password"`
}

type fileDevice struct {
	Namespace  string   `yaml:"namespace"`
	DeviceID   string   `yaml:"deviceId"`
	Endpoint   string   `yaml:"endpoint"`
	Credential string   `yaml:"credential"`
	Auth       fileAuth `yaml:"auth"`
	TLS        fileTLS  `yaml:"tls"`
}

type fileAuth struct {
	Mode string `yaml:"mode"`
}

type fileTLS struct {
	CAFile             string `yaml:"caFile"`
	ServerName         string `yaml:"serverName"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

type fileExecution struct {
	AllowPartialResults   bool      `yaml:"allowPartialResults"`
	AllowUnboundedFanout  bool      `yaml:"allowUnboundedFanout"`
	MaxDevicesPerQuery    int       `yaml:"maxDevicesPerQuery"`
	MaxConcurrentDevices  int       `yaml:"maxConcurrentDevices"`
	PerDeviceTimeout      string    `yaml:"perDeviceTimeout"`
	TotalTimeout          string    `yaml:"totalTimeout"`
	MaxRowsPerDevice      int       `yaml:"maxRowsPerDevice"`
	MaxRowsPerQuery       int       `yaml:"maxRowsPerQuery"`
	MaxPagesPerCollection int       `yaml:"maxPagesPerCollection"`
	MaxResponseBytes      string    `yaml:"maxResponseBytes"`
	BatchRows             int       `yaml:"batchRows"`
	Retry                 fileRetry `yaml:"retry"`
}

type fileRetry struct {
	MaxAttempts int    `yaml:"maxAttempts"`
	MinBackoff  string `yaml:"minBackoff"`
	MaxBackoff  string `yaml:"maxBackoff"`
}

type credentialDefinition struct {
	username secret.Reference
	password secret.Reference
}

type deviceDefinition struct {
	namespace      string
	id             string
	endpoint       string
	credentialName string
	authMode       device.AuthMode
	tls            device.TLSConfig
}

// Load reads one strict YAML document, validates it without network access and
// resolves all configured credentials into an immutable device registry.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open Redfish provider config: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var serialized fileConfig
	if err := decoder.Decode(&serialized); err != nil {
		return Config{}, fmt.Errorf("decode Redfish provider config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode Redfish provider config: multiple YAML documents are not supported")
		}
		return Config{}, fmt.Errorf("decode Redfish provider config: %w", err)
	}
	return normalize(serialized)
}

func normalize(serialized fileConfig) (Config, error) {
	listenAddress, err := normalizeListenAddress(serialized.Listen)
	if err != nil {
		return Config{}, err
	}
	profiles, err := normalizeProfiles(serialized.Profiles)
	if err != nil {
		return Config{}, err
	}
	execution, err := normalizeExecution(serialized.Execution)
	if err != nil {
		return Config{}, err
	}
	credentialDefinitions, err := normalizeCredentialDefinitions(serialized.Credentials)
	if err != nil {
		return Config{}, err
	}
	deviceDefinitions, err := normalizeDeviceDefinitions(serialized.Devices, credentialDefinitions)
	if err != nil {
		return Config{}, err
	}
	credentials, err := resolveCredentials(credentialDefinitions)
	if err != nil {
		return Config{}, err
	}

	configuredDevices := make([]device.Device, 0, len(deviceDefinitions))
	for _, definition := range deviceDefinitions {
		configuredDevice, err := device.New(device.Definition{
			Namespace:      definition.namespace,
			ID:             definition.id,
			Endpoint:       definition.endpoint,
			CredentialName: definition.credentialName,
			Credential:     credentials[definition.credentialName],
			AuthMode:       definition.authMode,
			TLS:            definition.tls,
		})
		if err != nil {
			return Config{}, fmt.Errorf("device %q: %w", definition.namespace+"/"+definition.id, err)
		}
		configuredDevices = append(configuredDevices, configuredDevice)
	}
	registry, err := device.NewRegistry(configuredDevices)
	if err != nil {
		return Config{}, err
	}

	warnings := make([]string, 0)
	for _, configuredDevice := range registry.Snapshot().Devices() {
		if configuredDevice.TLS().InsecureSkipVerify() {
			warnings = append(warnings, fmt.Sprintf(
				"device %q has tls.insecureSkipVerify enabled; server identity will not be verified",
				configuredDevice.Identity(),
			))
		}
	}
	return Config{
		listenAddress: listenAddress,
		profiles:      profiles,
		registry:      registry,
		execution:     execution,
		warnings:      warnings,
	}, nil
}

func normalizeListenAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		address = defaultListenAddress
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "", fmt.Errorf("invalid listen address %q: host contains whitespace", address)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid listen address %q: port must be between 1 and 65535", address)
	}
	return address, nil
}

func normalizeProfiles(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("at least one Redfish profile is required")
	}
	profiles := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		profile := strings.TrimSpace(value)
		if profile == "" {
			return nil, errors.New("Redfish profile must not be empty")
		}
		if profile != ProfileInventoryCore {
			return nil, fmt.Errorf("unsupported Redfish profile %q", profile)
		}
		if _, exists := seen[profile]; exists {
			return nil, fmt.Errorf("duplicate Redfish profile %q", profile)
		}
		seen[profile] = struct{}{}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func normalizeCredentialDefinitions(raw map[string]fileCredential) (map[string]credentialDefinition, error) {
	if len(raw) == 0 {
		return nil, errors.New("at least one credential is required")
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	normalized := make(map[string]credentialDefinition, len(raw))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, errors.New("credential name must not be empty")
		}
		if _, exists := normalized[name]; exists {
			return nil, fmt.Errorf("duplicate normalized credential name %q", name)
		}
		definition := raw[rawName]
		normalized[name] = credentialDefinition{
			username: definition.Username,
			password: definition.Password,
		}
	}
	return normalized, nil
}

func normalizeDeviceDefinitions(
	raw []fileDevice,
	credentials map[string]credentialDefinition,
) ([]deviceDefinition, error) {
	if len(raw) == 0 {
		return nil, errors.New("at least one Redfish device is required")
	}
	normalized := make([]deviceDefinition, 0, len(raw))
	identities := make(map[string]struct{}, len(raw))
	for index, value := range raw {
		namespace := strings.TrimSpace(value.Namespace)
		id := strings.TrimSpace(value.DeviceID)
		if namespace == "" {
			return nil, fmt.Errorf("devices[%d].namespace is required", index)
		}
		if id == "" {
			return nil, fmt.Errorf("devices[%d].deviceId is required", index)
		}
		identity := namespace + "\x00" + id
		if _, exists := identities[identity]; exists {
			return nil, fmt.Errorf("duplicate Redfish device identity %q", namespace+"/"+id)
		}
		identities[identity] = struct{}{}

		endpoint, err := normalizeEndpoint(value.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("device %q endpoint: %w", namespace+"/"+id, err)
		}
		credentialName := strings.TrimSpace(value.Credential)
		if credentialName == "" {
			return nil, fmt.Errorf("device %q credential is required", namespace+"/"+id)
		}
		if _, exists := credentials[credentialName]; !exists {
			return nil, fmt.Errorf("device %q references unknown credential %q", namespace+"/"+id, credentialName)
		}
		authMode, err := normalizeAuthMode(value.Auth.Mode)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", namespace+"/"+id, err)
		}
		normalized = append(normalized, deviceDefinition{
			namespace:      namespace,
			id:             id,
			endpoint:       endpoint,
			credentialName: credentialName,
			authMode:       authMode,
			tls: device.NewTLSConfig(
				value.TLS.CAFile,
				value.TLS.ServerName,
				value.TLS.InsecureSkipVerify,
			),
		})
	}
	return normalized, nil
}

func normalizeEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("invalid URL syntax")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("HTTPS URL is required")
	}
	if parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", errors.New("absolute URL with a host is required")
	}
	if parsed.User != nil {
		return "", errors.New("userinfo is not allowed")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("endpoint must identify an origin without a path")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("query parameters are not allowed")
	}
	if parsed.Fragment != "" {
		return "", errors.New("fragments are not allowed")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func normalizeAuthMode(raw string) (device.AuthMode, error) {
	mode := device.AuthMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		mode = device.AuthModeAuto
	}
	switch mode {
	case device.AuthModeAuto, device.AuthModeSession, device.AuthModeBasic:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid authentication mode %q", raw)
	}
}

func resolveCredentials(definitions map[string]credentialDefinition) (map[string]device.Credential, error) {
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	resolved := make(map[string]device.Credential, len(definitions))
	for _, name := range names {
		definition := definitions[name]
		username, err := definition.username.Resolve()
		if err != nil {
			return nil, fmt.Errorf("credential %q username: %w", name, err)
		}
		password, err := definition.password.Resolve()
		if err != nil {
			return nil, fmt.Errorf("credential %q password: %w", name, err)
		}
		resolved[name] = device.NewCredential(username, password)
	}
	return resolved, nil
}

func normalizeExecution(raw fileExecution) (ExecutionConfig, error) {
	var result ExecutionConfig
	result.AllowPartialResults = raw.AllowPartialResults
	result.AllowUnboundedFanout = raw.AllowUnboundedFanout
	var err error
	if result.MaxDevicesPerQuery, err = positiveIntOrDefault("execution.maxDevicesPerQuery", raw.MaxDevicesPerQuery, defaultMaxDevicesPerQuery); err != nil {
		return ExecutionConfig{}, err
	}
	if result.MaxConcurrentDevices, err = positiveIntOrDefault("execution.maxConcurrentDevices", raw.MaxConcurrentDevices, defaultMaxConcurrent); err != nil {
		return ExecutionConfig{}, err
	}
	if result.PerDeviceTimeout, err = durationOrDefault("execution.perDeviceTimeout", raw.PerDeviceTimeout, defaultPerDeviceTimeout); err != nil {
		return ExecutionConfig{}, err
	}
	if result.TotalTimeout, err = durationOrDefault("execution.totalTimeout", raw.TotalTimeout, defaultTotalTimeout); err != nil {
		return ExecutionConfig{}, err
	}
	if result.MaxRowsPerDevice, err = positiveIntOrDefault("execution.maxRowsPerDevice", raw.MaxRowsPerDevice, defaultMaxRowsPerDevice); err != nil {
		return ExecutionConfig{}, err
	}
	if result.MaxRowsPerQuery, err = positiveIntOrDefault("execution.maxRowsPerQuery", raw.MaxRowsPerQuery, defaultMaxRowsPerQuery); err != nil {
		return ExecutionConfig{}, err
	}
	if result.MaxPagesPerCollection, err = positiveIntOrDefault("execution.maxPagesPerCollection", raw.MaxPagesPerCollection, defaultMaxPages); err != nil {
		return ExecutionConfig{}, err
	}
	if result.MaxResponseBytes, err = byteSizeOrDefault("execution.maxResponseBytes", raw.MaxResponseBytes, defaultMaxResponseBytes); err != nil {
		return ExecutionConfig{}, err
	}
	if result.BatchRows, err = positiveIntOrDefault("execution.batchRows", raw.BatchRows, defaultBatchRows); err != nil {
		return ExecutionConfig{}, err
	}
	if result.Retry.MaxAttempts, err = positiveIntOrDefault("execution.retry.maxAttempts", raw.Retry.MaxAttempts, defaultRetryMaxAttempts); err != nil {
		return ExecutionConfig{}, err
	}
	if result.Retry.MinBackoff, err = durationOrDefault("execution.retry.minBackoff", raw.Retry.MinBackoff, defaultRetryMinimumBackoff); err != nil {
		return ExecutionConfig{}, err
	}
	if result.Retry.MaxBackoff, err = durationOrDefault("execution.retry.maxBackoff", raw.Retry.MaxBackoff, defaultRetryMaximumBackoff); err != nil {
		return ExecutionConfig{}, err
	}
	if result.Retry.MaxBackoff < result.Retry.MinBackoff {
		return ExecutionConfig{}, errors.New("execution.retry.maxBackoff must not be less than execution.retry.minBackoff")
	}
	return result, nil
}

func positiveIntOrDefault(name string, value int, defaultValue int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func durationOrDefault(name string, raw string, defaultValue time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func byteSizeOrDefault(name string, raw string, defaultValue int64) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultValue, nil
	}
	digitCount := 0
	for digitCount < len(value) && value[digitCount] >= '0' && value[digitCount] <= '9' {
		digitCount++
	}
	if digitCount == 0 {
		return 0, fmt.Errorf("parse %s: expected a positive byte count", name)
	}
	quantity, err := strconv.ParseUint(value[:digitCount], 10, 64)
	if err != nil || quantity == 0 {
		return 0, fmt.Errorf("parse %s: expected a positive byte count", name)
	}
	multipliers := map[string]uint64{
		"":    1,
		"B":   1,
		"KiB": 1 << 10,
		"MiB": 1 << 20,
		"GiB": 1 << 30,
	}
	multiplier, exists := multipliers[value[digitCount:]]
	if !exists {
		return 0, fmt.Errorf("parse %s: supported units are B, KiB, MiB and GiB", name)
	}
	if quantity > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("parse %s: value is too large", name)
	}
	return int64(quantity * multiplier), nil
}
