// Command redfish-schema-gen generates the provider-owned relational catalog
// from a locked, local DMTF JSON Schema subset and a declarative profile.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	defaultProfile = "schema/profiles/inventory-core.yaml"
	defaultLock    = "schema/upstream.lock.json"
	defaultOutput  = "internal/catalog/catalog_gen.go"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("redfish-schema-gen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	profile := flags.String("profile", defaultProfile, "profile path relative to the repository root")
	lock := flags.String("lock", defaultLock, "upstream lock path relative to the repository root")
	output := flags.String("output", defaultOutput, "generated Go path relative to the repository root")
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
	profilePath := resolvePath(rootPath, *profile)
	lockPath := resolvePath(rootPath, *lock)
	outputPath := resolvePath(rootPath, *output)
	generated, err := generate(profilePath, lockPath)
	if err != nil {
		return err
	}
	if *check {
		current, err := os.ReadFile(outputPath)
		if err != nil {
			return fmt.Errorf("read generated catalog %q: %w", outputPath, err)
		}
		if !bytes.Equal(current, generated) {
			return fmt.Errorf("generated catalog %q is stale; run go run ./cmd/redfish-schema-gen", outputPath)
		}
		return nil
	}
	return writeAtomic(outputPath, generated)
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
		return fmt.Errorf("create generated catalog directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".catalog-gen-*")
	if err != nil {
		return fmt.Errorf("create temporary generated catalog: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set generated catalog permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write generated catalog: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close generated catalog: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace generated catalog: %w", err)
	}
	return nil
}

func ensureEOF(decoder interface{ Decode(any) error }) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple documents are not supported")
	}
	return err
}
