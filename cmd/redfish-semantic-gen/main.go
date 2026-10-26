// Command redfish-semantic-gen generates the provider-owned source-local
// semantic fragment from the canonical Redfish metadata.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kubling-community/kubling-provider-redfish/internal/semanticmodel"
)

const defaultOutput = "internal/provider/semantic/redfish-inventory-v1.yaml"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("redfish-semantic-gen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	output := flags.String("output", defaultOutput, "generated YAML path relative to the repository root")
	check := flags.Bool("check", false, "verify generated output without writing it")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	rootPath, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	outputPath := resolvePath(rootPath, *output)
	document, err := semanticmodel.Generate()
	if err != nil {
		return err
	}
	if *check {
		current, err := os.ReadFile(outputPath)
		if err != nil {
			return fmt.Errorf("read generated semantic fragment %q: %w", outputPath, err)
		}
		if !bytes.Equal(current, document) {
			return fmt.Errorf(
				"generated semantic fragment %q is stale; run go run ./cmd/redfish-semantic-gen",
				outputPath,
			)
		}
		return nil
	}
	return writeAtomic(outputPath, document)
}

func resolvePath(root string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, filepath.Clean(path))
}

func writeAtomic(path string, contents []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create generated semantic directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".semantic-gen-*")
	if err != nil {
		return fmt.Errorf("create temporary semantic fragment: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set generated semantic fragment permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write generated semantic fragment: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close generated semantic fragment: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace generated semantic fragment: %w", err)
	}
	return nil
}
