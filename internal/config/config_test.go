package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secretCanary = "REDFISH-SECRET-CANARY-DO-NOT-LOG"

func TestLoadBuildsImmutableMultiDeviceSnapshot(t *testing.T) {
	t.Setenv("REDFISH_RACK_A_USERNAME", "operator-a")
	t.Setenv("REDFISH_RACK_A_PASSWORD", "password-a")
	t.Setenv("REDFISH_RACK_B_USERNAME", "operator-b")
	passwordFile := filepath.Join(t.TempDir(), "rack-b-password")
	if err := os.WriteFile(passwordFile, []byte("password-b\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	path := writeConfig(t, fmt.Sprintf(`
listen: "127.0.0.1:55051"
profiles:
  - inventory-core
credentials:
  rack_a:
    username:
      env: REDFISH_RACK_A_USERNAME
    password:
      env: REDFISH_RACK_A_PASSWORD
  rack_b:
    username:
      env: REDFISH_RACK_B_USERNAME
    password:
      file: %q
devices:
  - namespace: dc-b/rack-02
    deviceId: bmc-02
    endpoint: https://bmc-02.example.invalid
    credential: rack_b
    auth:
      mode: basic
    tls:
      caFile: /etc/redfish/rack-b-ca.pem
      serverName: bmc-02.internal
  - namespace: dc-a/rack-01
    deviceId: bmc-01
    endpoint: https://bmc-01.example.invalid
    credential: rack_a
execution:
  allowPartialResults: true
  maxDevicesPerQuery: 16
  maxConcurrentDevices: 4
  perDeviceTimeout: 10s
  totalTimeout: 30s
  maxRowsPerDevice: 2000
  maxRowsPerQuery: 5000
  maxPagesPerCollection: 50
  maxResponseBytes: 4MiB
  batchRows: 64
  retry:
    maxAttempts: 2
    minBackoff: 50ms
    maxBackoff: 500ms
`, passwordFile))

	configuration, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.ListenAddress() != "127.0.0.1:55051" {
		t.Fatalf("ListenAddress() = %q", configuration.ListenAddress())
	}
	profiles := configuration.Profiles()
	profiles[0] = "mutated"
	if got := configuration.Profiles(); len(got) != 1 || got[0] != ProfileInventoryCore {
		t.Fatalf("Profiles() after caller mutation = %v", got)
	}

	devices := configuration.Registry().Snapshot().Devices()
	if len(devices) != 2 || devices[0].Identity() != "dc-a/rack-01/bmc-01" || devices[1].Identity() != "dc-b/rack-02/bmc-02" {
		t.Fatalf("registry devices = %v", devices)
	}
	if devices[0].AuthMode() != "auto" || devices[0].TLS().InsecureSkipVerify() || devices[0].TLS().CAFile() != "" {
		t.Fatalf("device zero defaults = auth %q TLS %+v", devices[0].AuthMode(), devices[0].TLS())
	}
	if devices[0].Credential().Username().Reveal() != "operator-a" || devices[0].Credential().Password().Reveal() != "password-a" {
		t.Fatal("device zero did not receive rack_a credentials")
	}
	if devices[1].AuthMode() != "basic" || devices[1].TLS().CAFile() != "/etc/redfish/rack-b-ca.pem" || devices[1].TLS().ServerName() != "bmc-02.internal" {
		t.Fatalf("device one settings = auth %q TLS %+v", devices[1].AuthMode(), devices[1].TLS())
	}
	if devices[1].Credential().Username().Reveal() != "operator-b" || devices[1].Credential().Password().Reveal() != "password-b" {
		t.Fatal("device one did not receive rack_b credentials")
	}

	execution := configuration.Execution()
	if !execution.AllowPartialResults || execution.AllowUnboundedFanout || execution.MaxDevicesPerQuery != 16 ||
		execution.MaxConcurrentDevices != 4 || execution.PerDeviceTimeout != 10*time.Second ||
		execution.TotalTimeout != 30*time.Second || execution.MaxRowsPerDevice != 2000 ||
		execution.MaxRowsPerQuery != 5000 || execution.MaxPagesPerCollection != 50 ||
		execution.MaxResponseBytes != 4*1024*1024 || execution.BatchRows != 64 ||
		execution.Retry.MaxAttempts != 2 || execution.Retry.MinBackoff != 50*time.Millisecond ||
		execution.Retry.MaxBackoff != 500*time.Millisecond {
		t.Fatalf("Execution() = %+v", execution)
	}
	if len(configuration.Warnings()) != 0 {
		t.Fatalf("Warnings() = %v", configuration.Warnings())
	}
}

func TestLoadAppliesSecureTLSAndBoundedExecutionDefaults(t *testing.T) {
	path := writeMinimalConfig(t, "https://127.0.0.1:1", false)
	configuration, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	configuredDevice := configuration.Registry().Snapshot().Devices()[0]
	if configuredDevice.TLS().InsecureSkipVerify() || configuredDevice.TLS().CAFile() != "" || configuredDevice.TLS().ServerName() != "" {
		t.Fatalf("TLS defaults = %+v", configuredDevice.TLS())
	}
	if configuration.ListenAddress() != defaultListenAddress {
		t.Fatalf("ListenAddress() = %q, want %q", configuration.ListenAddress(), defaultListenAddress)
	}
	execution := configuration.Execution()
	if execution.MaxDevicesPerQuery != defaultMaxDevicesPerQuery ||
		execution.MaxConcurrentDevices != defaultMaxConcurrent ||
		execution.PerDeviceTimeout != defaultPerDeviceTimeout ||
		execution.TotalTimeout != defaultTotalTimeout ||
		execution.MaxRowsPerDevice != defaultMaxRowsPerDevice ||
		execution.MaxRowsPerQuery != defaultMaxRowsPerQuery ||
		execution.MaxPagesPerCollection != defaultMaxPages ||
		execution.MaxResponseBytes != defaultMaxResponseBytes ||
		execution.BatchRows != defaultBatchRows ||
		execution.Retry.MaxAttempts != defaultRetryMaxAttempts ||
		execution.Retry.MinBackoff != defaultRetryMinimumBackoff ||
		execution.Retry.MaxBackoff != defaultRetryMaximumBackoff {
		t.Fatalf("default Execution() = %+v", execution)
	}
}

func TestLoadRejectsDuplicateIdentityBeforeResolvingSecrets(t *testing.T) {
	t.Setenv("REDFISH_TEST_USERNAME", secretCanary)
	t.Setenv("REDFISH_TEST_PASSWORD", secretCanary)
	path := writeConfig(t, `
profiles: [inventory-core]
credentials:
  test:
    username: {env: REDFISH_TEST_USERNAME}
    password: {env: REDFISH_TEST_PASSWORD}
devices:
  - namespace: dc-a
    deviceId: bmc-01
    endpoint: https://first.example.invalid
    credential: test
  - namespace: dc-a
    deviceId: bmc-01
    endpoint: https://second.example.invalid
    credential: test
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate Redfish device identity") {
		t.Fatalf("Load() error = %v", err)
	}
	assertNoCanary(t, err)
}

func TestLoadRejectsInvalidEndpointsWithoutExposingSecrets(t *testing.T) {
	tests := map[string]string{
		"plaintext": "http://bmc.example.invalid",
		"relative":  "/redfish/v1",
		"userinfo":  "https://user:password@bmc.example.invalid",
		"path":      "https://bmc.example.invalid/redfish/v1",
		"query":     "https://bmc.example.invalid?secret=value",
		"fragment":  "https://bmc.example.invalid#fragment",
		"malformed": "https://" + secretCanary + "%zz",
	}
	for name, endpoint := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeMinimalConfig(t, endpoint, false)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "endpoint") {
				t.Fatalf("Load() error = %v", err)
			}
			assertNoCanary(t, err)
		})
	}
}

func TestLoadIsStrictAndAcceptsOneYAMLDocument(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		path := writeConfig(t, "profiles: [inventory-core]\nunknownField: true\n")
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "field unknownField not found") {
			t.Fatalf("Load() error = %v", err)
		}
	})
	t.Run("multiple documents", func(t *testing.T) {
		path := writeConfig(t, "profiles: [inventory-core]\n---\nprofiles: [inventory-core]\n")
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadRejectsInlineSecretsAndNeverPrintsResolvedCanary(t *testing.T) {
	inlinePath := writeConfig(t, `
profiles: [inventory-core]
credentials:
  test:
    username: inline-username-canary
    password: inline-password-canary
devices:
  - namespace: dc-a
    deviceId: bmc-01
    endpoint: https://bmc.example.invalid
    credential: test
`)
	_, err := Load(inlinePath)
	if err == nil {
		t.Fatal("Load(inline secrets) error = nil")
	}
	if strings.Contains(err.Error(), "inline-username-canary") || strings.Contains(err.Error(), "inline-password-canary") {
		t.Fatalf("inline-secret validation error exposes candidate secret: %v", err)
	}

	t.Setenv("REDFISH_TEST_USERNAME", secretCanary)
	t.Setenv("REDFISH_TEST_PASSWORD", secretCanary)
	validPath := writeMinimalConfig(t, "https://bmc.example.invalid", true)
	configuration, err := Load(validPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	formatted := fmt.Sprintf("%v %#v %v %#v", configuration, configuration, configuration.Registry(), configuration.Registry().Snapshot())
	if strings.Contains(formatted, secretCanary) {
		t.Fatalf("formatted configuration exposes resolved secret: %q", formatted)
	}
	warnings := configuration.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "insecureSkipVerify") {
		t.Fatalf("Warnings() = %v", warnings)
	}
}

func writeMinimalConfig(t *testing.T, endpoint string, insecureSkipVerify bool) string {
	t.Helper()
	t.Setenv("REDFISH_TEST_USERNAME", secretCanary)
	t.Setenv("REDFISH_TEST_PASSWORD", secretCanary)
	return writeConfig(t, fmt.Sprintf(`
profiles: [inventory-core]
credentials:
  test:
    username: {env: REDFISH_TEST_USERNAME}
    password: {env: REDFISH_TEST_PASSWORD}
devices:
  - namespace: dc-a
    deviceId: bmc-01
    endpoint: %q
    credential: test
    tls:
      insecureSkipVerify: %t
`, endpoint, insecureSkipVerify))
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "redfish.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}

func assertNoCanary(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), secretCanary) {
		t.Fatalf("error exposes secret canary: %v", err)
	}
}
