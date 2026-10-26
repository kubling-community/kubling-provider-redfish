// Package secret resolves references to runtime secrets without exposing their
// contents through formatting or validation errors.
package secret

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const redacted = "[REDACTED]"

// Reference identifies one secret stored in an environment variable or file.
// Inline secret values are deliberately unsupported.
type Reference struct {
	environment string
	file        string
}

// UnmarshalYAML decodes a strict, single-source secret reference.
func (r *Reference) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("secret reference must be a mapping with exactly one of env or file")
	}

	var decoded Reference
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		name := key.Value
		if _, exists := seen[name]; exists {
			return fmt.Errorf("secret reference field %q is duplicated", name)
		}
		seen[name] = struct{}{}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return fmt.Errorf("secret reference field %q must be a string", name)
		}
		source := strings.TrimSpace(value.Value)
		switch name {
		case "env":
			decoded.environment = source
		case "file":
			decoded.file = source
		default:
			return fmt.Errorf("secret reference contains unknown field %q", name)
		}
	}

	if (decoded.environment == "") == (decoded.file == "") {
		return errors.New("secret reference must set exactly one of env or file")
	}
	*r = decoded
	return nil
}

// Resolve reads the referenced secret. File-backed values have only trailing
// line endings removed, matching common mounted-secret files.
func (r Reference) Resolve() (Value, error) {
	switch {
	case r.environment != "" && r.file == "":
		raw, exists := os.LookupEnv(r.environment)
		if !exists {
			return Value{}, fmt.Errorf("environment variable %q is not set", r.environment)
		}
		return NewValue(raw)
	case r.file != "" && r.environment == "":
		contents, err := os.ReadFile(r.file)
		if err != nil {
			return Value{}, fmt.Errorf("read secret file %q: %w", r.file, err)
		}
		return NewValue(strings.TrimRight(string(contents), "\r\n"))
	default:
		return Value{}, errors.New("secret reference must set exactly one of env or file")
	}
}

// String returns a safe description of the reference.
func (r Reference) String() string {
	if r.environment != "" {
		return fmt.Sprintf("environment variable %q", r.environment)
	}
	if r.file != "" {
		return fmt.Sprintf("file %q", r.file)
	}
	return "invalid secret reference"
}

// GoString prevents %#v from exposing implementation details.
func (r Reference) GoString() string {
	return r.String()
}

// Value holds a resolved secret. Its textual representations are always
// redacted; Reveal is intentionally explicit for authenticated clients.
type Value struct {
	raw string
}

// NewValue constructs a non-empty secret value.
func NewValue(raw string) (Value, error) {
	if raw == "" {
		return Value{}, errors.New("secret value is empty")
	}
	return Value{raw: raw}, nil
}

// Reveal returns the secret to code that must authenticate to a device.
func (v Value) Reveal() string {
	return v.raw
}

// String redacts the secret.
func (Value) String() string {
	return redacted
}

// GoString redacts the secret when formatted with %#v.
func (Value) GoString() string {
	return redacted
}
