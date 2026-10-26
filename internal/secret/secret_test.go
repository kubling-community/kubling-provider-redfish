package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReferenceResolvesEnvironmentAndFileSecrets(t *testing.T) {
	t.Setenv("REDFISH_TEST_USERNAME", "operator")
	secretFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(secretFile, []byte("file-secret\r\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	var references struct {
		Username Reference `yaml:"username"`
		Password Reference `yaml:"password"`
	}
	document := fmt.Sprintf(
		"username:\n  env: REDFISH_TEST_USERNAME\npassword:\n  file: %q\n",
		secretFile,
	)
	if err := yaml.Unmarshal([]byte(document), &references); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	username, err := references.Username.Resolve()
	if err != nil {
		t.Fatalf("Username.Resolve() error = %v", err)
	}
	password, err := references.Password.Resolve()
	if err != nil {
		t.Fatalf("Password.Resolve() error = %v", err)
	}
	if username.Reveal() != "operator" || password.Reveal() != "file-secret" {
		t.Fatalf("resolved values = (%q, %q)", username.Reveal(), password.Reveal())
	}
}

func TestReferenceRejectsInlineMultipleAndUnknownSources(t *testing.T) {
	tests := map[string]string{
		"inline":  "INLINE-CANARY",
		"both":    "env: USER_CANARY\nfile: /tmp/file-canary\n",
		"unknown": "vault: SECRET-CANARY\n",
		"empty":   "{}\n",
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			var reference Reference
			err := yaml.Unmarshal([]byte(document), &reference)
			if err == nil {
				t.Fatal("yaml.Unmarshal() error = nil")
			}
			if strings.Contains(err.Error(), "INLINE-CANARY") || strings.Contains(err.Error(), "SECRET-CANARY") {
				t.Fatalf("error exposes candidate secret: %v", err)
			}
		})
	}
}

func TestValueFormattingIsAlwaysRedacted(t *testing.T) {
	const canary = "DO-NOT-PRINT-SECRET"
	value, err := NewValue(canary)
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	formatted := fmt.Sprintf("%s %v %+v %#v", value, value, value, value)
	if strings.Contains(formatted, canary) {
		t.Fatalf("formatted secret = %q", formatted)
	}
	if strings.Count(formatted, redacted) != 4 {
		t.Fatalf("formatted secret = %q, want four redactions", formatted)
	}
}

func TestReferenceRejectsMissingAndEmptySources(t *testing.T) {
	var missing Reference
	if _, err := missing.Resolve(); err == nil {
		t.Fatal("zero Reference.Resolve() error = nil")
	}

	t.Setenv("REDFISH_EMPTY_SECRET", "")
	var empty Reference
	if err := yaml.Unmarshal([]byte("env: REDFISH_EMPTY_SECRET\n"), &empty); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if _, err := empty.Resolve(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty Resolve() error = %v", err)
	}
}
