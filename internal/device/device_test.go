package device

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
)

func TestRegistryRejectsDuplicateNamespaceAndDeviceID(t *testing.T) {
	first := testDevice(t, "dc-a/rack-01", "bmc-01", "first-secret")
	duplicate := testDevice(t, "dc-a/rack-01", "bmc-01", "second-secret")
	if _, err := NewRegistry([]Device{first, duplicate}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("NewRegistry() error = %v, want duplicate identity", err)
	}
}

func TestRegistrySnapshotsAreDeterministicAndDetached(t *testing.T) {
	second := testDevice(t, "dc-b", "bmc-02", "second-secret")
	first := testDevice(t, "dc-a", "bmc-01", "first-secret")
	registry, err := NewRegistry([]Device{second, first})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	devices := registry.Snapshot().Devices()
	if len(devices) != 2 || devices[0].Identity() != "dc-a/bmc-01" || devices[1].Identity() != "dc-b/bmc-02" {
		t.Fatalf("Snapshot().Devices() = %v", devices)
	}
	devices[0] = Device{}
	fresh := registry.Snapshot().Devices()
	if fresh[0].Identity() != "dc-a/bmc-01" {
		t.Fatalf("snapshot mutation changed registry: %v", fresh)
	}
	found, exists := registry.Find("dc-b", "bmc-02")
	if !exists || found.Credential().Password().Reveal() != "second-secret" {
		t.Fatalf("Find() = (%v, %v)", found, exists)
	}
}

func TestDeviceAndRegistryFormattingNeverExposeCredentials(t *testing.T) {
	const canary = "DEVICE-CREDENTIAL-CANARY"
	configuredDevice := testDevice(t, "dc-a", "bmc-01", canary)
	registry, err := NewRegistry([]Device{configuredDevice})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	formatted := fmt.Sprintf(
		"%v %#v %v %#v %v %#v",
		configuredDevice,
		configuredDevice,
		configuredDevice.Credential(),
		configuredDevice.Credential(),
		registry,
		registry.Snapshot(),
	)
	if strings.Contains(formatted, canary) {
		t.Fatalf("formatted device data exposes credential: %q", formatted)
	}
}

func testDevice(t *testing.T, namespace string, id string, password string) Device {
	t.Helper()
	usernameValue, err := secret.NewValue("operator-" + id)
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	passwordValue, err := secret.NewValue(password)
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configuredDevice, err := New(Definition{
		Namespace:      namespace,
		ID:             id,
		Endpoint:       "https://" + id + ".example.invalid",
		CredentialName: "credential-" + id,
		Credential:     NewCredential(usernameValue, passwordValue),
		AuthMode:       AuthModeAuto,
		TLS:            NewTLSConfig("", "", false),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return configuredDevice
}
