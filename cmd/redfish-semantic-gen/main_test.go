package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubling-community/kubling-provider-redfish/internal/semanticmodel"
)

func TestCheckAcceptsGeneratedBytesAndDetectsDrift(t *testing.T) {
	document, err := semanticmodel.Generate()
	if err != nil {
		t.Fatalf("semanticmodel.Generate() error = %v", err)
	}
	output := filepath.Join(t.TempDir(), "redfish-inventory-v1.yaml")
	if err := os.WriteFile(output, document, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	var stderr bytes.Buffer
	if err := run([]string{"--check", "--output", output}, &stderr); err != nil {
		t.Fatalf("run(--check) error = %v; stderr = %s", err, stderr.String())
	}
	if err := os.WriteFile(output, append(document, '\n'), 0o600); err != nil {
		t.Fatalf("os.WriteFile(drift) error = %v", err)
	}
	if err := run([]string{"--check", "--output", output}, &stderr); err == nil ||
		!strings.Contains(err.Error(), "is stale") {
		t.Fatalf("run(--check drift) error = %v, want stale", err)
	}
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	var stderr bytes.Buffer
	if err := run([]string{"unexpected"}, &stderr); err == nil ||
		!strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("run(unexpected) error = %v", err)
	}
}
