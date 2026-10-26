// Package device defines immutable Redfish device configuration and registry
// snapshots.
package device

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
)

// AuthMode selects Redfish authentication behavior.
type AuthMode string

const (
	// AuthModeAuto prefers session authentication and permits a Basic fallback
	// only when the Redfish Session Service is unsupported.
	AuthModeAuto AuthMode = "auto"
	// AuthModeSession requires Redfish session authentication.
	AuthModeSession AuthMode = "session"
	// AuthModeBasic requires HTTP Basic authentication.
	AuthModeBasic AuthMode = "basic"
)

// Credential is an immutable username/password pair.
type Credential struct {
	username secret.Value
	password secret.Value
}

// NewCredential creates a credential from resolved secret values.
func NewCredential(username secret.Value, password secret.Value) Credential {
	return Credential{username: username, password: password}
}

// Username returns the resolved username secret.
func (c Credential) Username() secret.Value {
	return c.username
}

// Password returns the resolved password secret.
func (c Credential) Password() secret.Value {
	return c.password
}

// String always redacts the credential.
func (Credential) String() string {
	return "[REDACTED CREDENTIAL]"
}

// GoString always redacts the credential.
func (Credential) GoString() string {
	return "[REDACTED CREDENTIAL]"
}

// TLSConfig contains per-device server verification settings.
type TLSConfig struct {
	caFile             string
	serverName         string
	insecureSkipVerify bool
}

// NewTLSConfig creates immutable per-device TLS settings.
func NewTLSConfig(caFile string, serverName string, insecureSkipVerify bool) TLSConfig {
	return TLSConfig{
		caFile:             strings.TrimSpace(caFile),
		serverName:         strings.TrimSpace(serverName),
		insecureSkipVerify: insecureSkipVerify,
	}
}

// CAFile returns the optional PEM CA bundle path.
func (c TLSConfig) CAFile() string {
	return c.caFile
}

// ServerName returns the optional TLS server-name override.
func (c TLSConfig) ServerName() string {
	return c.serverName
}

// InsecureSkipVerify reports whether server verification was explicitly disabled.
func (c TLSConfig) InsecureSkipVerify() bool {
	return c.insecureSkipVerify
}

// Definition contains validated inputs for one immutable device.
type Definition struct {
	Namespace      string
	ID             string
	Endpoint       string
	CredentialName string
	Credential     Credential
	AuthMode       AuthMode
	TLS            TLSConfig
}

// Device is an immutable Redfish endpoint and its resolved authentication data.
type Device struct {
	namespace      string
	id             string
	endpoint       string
	credentialName string
	credential     Credential
	authMode       AuthMode
	tls            TLSConfig
}

// New constructs one immutable device from validated inputs.
func New(definition Definition) (Device, error) {
	namespace := strings.TrimSpace(definition.Namespace)
	id := strings.TrimSpace(definition.ID)
	endpoint := strings.TrimSpace(definition.Endpoint)
	credentialName := strings.TrimSpace(definition.CredentialName)
	if namespace == "" {
		return Device{}, errors.New("device namespace is required")
	}
	if id == "" {
		return Device{}, errors.New("device ID is required")
	}
	if endpoint == "" {
		return Device{}, errors.New("device endpoint is required")
	}
	if credentialName == "" {
		return Device{}, errors.New("device credential name is required")
	}
	switch definition.AuthMode {
	case AuthModeAuto, AuthModeSession, AuthModeBasic:
	default:
		return Device{}, fmt.Errorf("invalid device authentication mode %q", definition.AuthMode)
	}
	return Device{
		namespace:      namespace,
		id:             id,
		endpoint:       endpoint,
		credentialName: credentialName,
		credential:     definition.Credential,
		authMode:       definition.AuthMode,
		tls:            definition.TLS,
	}, nil
}

// Namespace returns the fleet namespace containing this device.
func (d Device) Namespace() string {
	return d.namespace
}

// ID returns the device identifier within its namespace.
func (d Device) ID() string {
	return d.id
}

// Identity returns a stable human-readable namespace/device identifier.
func (d Device) Identity() string {
	return d.namespace + "/" + d.id
}

// Endpoint returns the Redfish service endpoint.
func (d Device) Endpoint() string {
	return d.endpoint
}

// CredentialName returns the non-secret configured credential profile name.
func (d Device) CredentialName() string {
	return d.credentialName
}

// Credential returns a detached immutable credential value.
func (d Device) Credential() Credential {
	return d.credential
}

// AuthMode returns the configured authentication mode.
func (d Device) AuthMode() AuthMode {
	return d.authMode
}

// TLS returns detached immutable TLS settings.
func (d Device) TLS() TLSConfig {
	return d.tls
}

// String formats only non-secret device metadata.
func (d Device) String() string {
	return fmt.Sprintf("Redfish device %q at %q", d.Identity(), d.endpoint)
}

// GoString formats only non-secret device metadata.
func (d Device) GoString() string {
	return d.String()
}

// Registry owns the validated immutable set of configured Redfish devices.
type Registry struct {
	devices []Device
	byKey   map[string]int
}

// NewRegistry validates device identity uniqueness and creates a deterministic registry.
func NewRegistry(devices []Device) (*Registry, error) {
	if len(devices) == 0 {
		return nil, errors.New("at least one Redfish device is required")
	}
	owned := append([]Device(nil), devices...)
	sort.Slice(owned, func(left int, right int) bool {
		if owned[left].namespace == owned[right].namespace {
			return owned[left].id < owned[right].id
		}
		return owned[left].namespace < owned[right].namespace
	})
	byKey := make(map[string]int, len(owned))
	for index, configuredDevice := range owned {
		key := identityKey(configuredDevice.namespace, configuredDevice.id)
		if _, exists := byKey[key]; exists {
			return nil, fmt.Errorf("duplicate Redfish device identity %q", configuredDevice.Identity())
		}
		byKey[key] = index
	}
	return &Registry{devices: owned, byKey: byKey}, nil
}

// Len returns the number of configured devices.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.devices)
}

// Find returns a detached device value by namespace and ID.
func (r *Registry) Find(namespace string, id string) (Device, bool) {
	if r == nil {
		return Device{}, false
	}
	index, exists := r.byKey[identityKey(namespace, id)]
	if !exists {
		return Device{}, false
	}
	return r.devices[index], true
}

// Snapshot returns an immutable point-in-time copy of the registry.
func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	return Snapshot{devices: append([]Device(nil), r.devices...)}
}

// String formats registry cardinality without credentials.
func (r *Registry) String() string {
	return fmt.Sprintf("Redfish device registry (%d devices)", r.Len())
}

// GoString formats registry cardinality without credentials.
func (r *Registry) GoString() string {
	return r.String()
}

// Snapshot is a detached, immutable registry view.
type Snapshot struct {
	devices []Device
}

// Len returns the snapshot device count.
func (s Snapshot) Len() int {
	return len(s.devices)
}

// Devices returns a detached, deterministically ordered device slice.
func (s Snapshot) Devices() []Device {
	return append([]Device(nil), s.devices...)
}

// String formats snapshot cardinality without credentials.
func (s Snapshot) String() string {
	return fmt.Sprintf("Redfish device snapshot (%d devices)", s.Len())
}

// GoString formats snapshot cardinality without credentials.
func (s Snapshot) GoString() string {
	return s.String()
}

func identityKey(namespace string, id string) string {
	return namespace + "\x00" + id
}
