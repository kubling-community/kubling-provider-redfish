package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadProfile(path string) (profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return profile{}, fmt.Errorf("open catalog profile %q: %w", path, err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var result profile
	if err := decoder.Decode(&result); err != nil {
		return profile{}, fmt.Errorf("decode catalog profile %q: %w", path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return profile{}, fmt.Errorf("decode catalog profile %q: %w", path, err)
	}
	return result, nil
}

func loadSchemaSet(lockPath string) (*schemaSet, error) {
	lock, err := loadLock(lockPath)
	if err != nil {
		return nil, err
	}
	upstreamRoot := filepath.Join(filepath.Dir(lockPath), "upstream")
	locked, err := verifyLock(upstreamRoot, lock)
	if err != nil {
		return nil, err
	}
	documents := make(map[string]schemaDocument)
	for _, lockedFile := range lock.Files {
		if filepath.Ext(lockedFile.Path) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(upstreamRoot, filepath.FromSlash(lockedFile.Path)))
		if err != nil {
			return nil, fmt.Errorf("read locked schema %q: %w", lockedFile.Path, err)
		}
		document, err := decodeJSONObject(contents)
		if err != nil {
			return nil, fmt.Errorf("decode locked schema %q: %w", lockedFile.Path, err)
		}
		name := filepath.Base(lockedFile.Path)
		if _, exists := documents[name]; exists {
			return nil, fmt.Errorf("duplicate locked schema filename %q", name)
		}
		documents[name] = schemaDocument{name: name, root: document}
	}
	return &schemaSet{lock: lock, documents: documents, locked: locked}, nil
}

func loadLock(path string) (upstreamLock, error) {
	file, err := os.Open(path)
	if err != nil {
		return upstreamLock{}, fmt.Errorf("open upstream lock %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var result upstreamLock
	if err := decoder.Decode(&result); err != nil {
		return upstreamLock{}, fmt.Errorf("decode upstream lock %q: %w", path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return upstreamLock{}, fmt.Errorf("decode upstream lock %q: %w", path, err)
	}
	return result, nil
}

func verifyLock(upstreamRoot string, lock upstreamLock) (map[string]struct{}, error) {
	if lock.FormatVersion != 1 {
		return nil, fmt.Errorf("unsupported upstream lock format %d", lock.FormatVersion)
	}
	if strings.TrimSpace(lock.Repository) == "" || strings.TrimSpace(lock.Release) == "" ||
		!commitPattern.MatchString(lock.Commit) || lock.License != "BSD-3-Clause" {
		return nil, errors.New("upstream lock identity is incomplete or invalid")
	}
	if len(lock.Files) == 0 {
		return nil, errors.New("upstream lock contains no files")
	}
	locked := make(map[string]struct{}, len(lock.Files))
	previous := ""
	for index, entry := range lock.Files {
		path := filepath.ToSlash(filepath.Clean(entry.Path))
		if path != entry.Path || path == "." || filepath.IsAbs(entry.Path) ||
			path == ".." || strings.HasPrefix(path, "../") {
			return nil, fmt.Errorf("upstream lock file %d has unsafe path %q", index, entry.Path)
		}
		if index > 0 && path <= previous {
			return nil, errors.New("upstream lock files must be strictly sorted by path")
		}
		previous = path
		if _, exists := locked[path]; exists {
			return nil, fmt.Errorf("upstream lock repeats %q", path)
		}
		digest, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(digest) != sha256.Size || entry.SHA256 != strings.ToLower(entry.SHA256) {
			return nil, fmt.Errorf("upstream lock file %q has invalid SHA-256", path)
		}
		fullPath := filepath.Join(upstreamRoot, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if err != nil {
			return nil, fmt.Errorf("inspect locked upstream file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("locked upstream file %q is not a regular file", path)
		}
		contents, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read locked upstream file %q: %w", path, err)
		}
		actual := sha256.Sum256(contents)
		if !strings.EqualFold(hex.EncodeToString(actual[:]), entry.SHA256) {
			return nil, fmt.Errorf("locked upstream file %q does not match SHA-256 %s", path, entry.SHA256)
		}
		locked[path] = struct{}{}
	}
	if _, exists := locked["LICENSE.md"]; !exists {
		return nil, errors.New("upstream lock must retain LICENSE.md")
	}
	if err := rejectUnlockedUpstreamFiles(upstreamRoot, locked); err != nil {
		return nil, err
	}
	return locked, nil
}

func rejectUnlockedUpstreamFiles(upstreamRoot string, locked map[string]struct{}) error {
	var unexpected []string
	err := filepath.WalkDir(upstreamRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(upstreamRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "README.md" {
			return nil
		}
		if _, exists := locked[relative]; !exists {
			unexpected = append(unexpected, relative)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect vendored upstream directory: %w", err)
	}
	if len(unexpected) != 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("vendored upstream files are not locked: %s", strings.Join(unexpected, ", "))
	}
	return nil
}
