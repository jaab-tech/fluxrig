// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

type DefaultManager struct {
	store   cas.Store
	rootDir string
	index   *Index
	mu      sync.RWMutex
}

// Index maps Logical Names to Tags to Content Hashes
// Example: "visa" -> "v1.0" -> "sha256:..."
type Index struct {
	Specs     map[string]map[string]string `json:"specs"`
	Scenarios map[string]map[string]string `json:"scenarios"`
}

func NewManager(rootDir string) (*DefaultManager, error) {
	store, err := cas.NewDiskStore(rootDir)
	if err != nil {
		return nil, err
	}

	m := &DefaultManager{
		store:   store,
		rootDir: rootDir,
		index: &Index{
			Specs:     make(map[string]map[string]string),
			Scenarios: make(map[string]map[string]string),
		},
	}

	if err := m.loadIndex(); err != nil {
		return nil, err
	}
	return m, nil
}

// loadIndex reads the persisted index from disk.
// TODO(multi-process): Add flock() or atomic-swap for safe concurrent CLI + Mixer access.
func (m *DefaultManager) loadIndex() error {
	path := filepath.Join(m.rootDir, "index.json")
	data, err := os.ReadFile(filepath.Clean(path))
	if os.IsNotExist(err) {
		return nil // Start fresh
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, m.index); err != nil {
		return fmt.Errorf("corrupt index.json: %w", err)
	}
	return nil
}

func (m *DefaultManager) saveIndex() error {
	path := filepath.Join(m.rootDir, "index.json")
	data, err := json.MarshalIndent(m.index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// SpecHeader captures metadata from the YAML file
type SpecHeader struct {
	Name    string                 `yaml:"name"`
	Version string                 `yaml:"version"`
	Meta    map[string]interface{} `yaml:"metadata,omitempty"`
}

// Import snapshots a local spec file into the CAS.
func (m *DefaultManager) Import(ctx context.Context, filePath, nameOverride, tagOverride string) (string, string, string, error) {
	return m.importArtifact(filePath, nameOverride, tagOverride, m.index.Specs, "spec")
}

// ImportScenario snapshots a local scenario file into the CAS.
func (m *DefaultManager) ImportScenario(ctx context.Context, filePath, nameOverride, tagOverride string) (string, string, string, error) {
	return m.importArtifact(filePath, nameOverride, tagOverride, m.index.Scenarios, "scenario")
}

// importArtifact is the shared import logic for specs and scenarios.
func (m *DefaultManager) importArtifact(filePath, nameOverride, tagOverride string, index map[string]map[string]string, kind string) (string, string, string, error) {
	// G304: Potential file inclusion via variable
	cleanPath := filepath.Clean(filePath)
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read file: %w", err)
	}

	// 1. Parse Metadata from Content (Best Effort)
	var header SpecHeader
	_ = yaml.Unmarshal(content, &header)

	// 2. Determine Name
	finalName := nameOverride
	if finalName == "" {
		finalName = header.Name
	}
	if finalName == "" {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		finalName = base[:len(base)-len(ext)]
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 3. Determine Tag (Version)
	finalTag := tagOverride
	if finalTag == "" {
		finalTag = header.Version
	}
	if finalTag == "" {
		nextVer := m.calcNextMinorFrom(index, finalName)
		finalTag = nextVer
	}

	// 4. Validate Tag (Semantic Versioning)
	if !semver.IsValid(finalTag) {
		return "", "", "", fmt.Errorf("invalid semantic version: %s (must start with 'v' and follow semver)", finalTag)
	}

	// 5. Store in CAS
	hash, err := m.store.Put(content)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to store content: %w", err)
	}

	// 6. Update Index (with Conflict Check)
	if index[finalName] == nil {
		index[finalName] = make(map[string]string)
	}

	existingHash, exists := index[finalName][finalTag]
	if exists {
		if existingHash != hash {
			return "", "", "", fmt.Errorf("version %s:%s already exists with different content (immutable). Please increment version", finalName, finalTag)
		}
	}

	index[finalName][finalTag] = hash
	// Recompute "latest" as the highest semver — not "last imported"
	index[finalName]["latest"] = m.resolveLatestHash(index[finalName])

	// 7. Persist Index
	if err := m.saveIndex(); err != nil {
		return "", "", "", fmt.Errorf("failed to save index: %w", err)
	}

	ref := cas.Ref(finalName, finalTag, hash)
	slog.Info("CAS import complete", "kind", kind, "cas_ref", ref)
	return hash, finalName, finalTag, nil
}

// Export resolves a URN and writes its content to the given output path.
func (m *DefaultManager) Export(ctx context.Context, urn, outputPath string) error {
	content, err := m.Load(ctx, urn)
	if err != nil {
		return fmt.Errorf("failed to resolve URN %s: %w", urn, err)
	}

	cleanPath := filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := os.WriteFile(cleanPath, content, 0600); err != nil {
		return fmt.Errorf("failed to write export file: %w", err)
	}

	slog.Info("CAS export complete", "urn", urn, "output", cleanPath)
	return nil
}

// calcNextMinorFrom finds the latest version in the given index and increments minor.
func (m *DefaultManager) calcNextMinorFrom(index map[string]map[string]string, name string) string {
	tags := index[name]
	if len(tags) == 0 {
		return "v0.1.0"
	}

	latest := m.resolveLatest(tags)
	if latest == "" {
		return "v0.1.0"
	}

	c := semver.Canonical(latest)
	if c == "" {
		return "v0.1.0"
	}

	var major, minor, patch int
	n, err := fmt.Sscanf(c, "v%d.%d.%d", &major, &minor, &patch)
	if err != nil || n != 3 {
		return "v0.1.0"
	}

	minor++
	patch = 0
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch)
}

func (m *DefaultManager) Load(ctx context.Context, urn string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 1. Check if URN is already a hash
	if m.store.Has(urn) {
		return m.store.Get(urn)
	}

	// 2. Parse URN (Format: "name:tag")
	// For now, strict "name:tag" format. Future: "urn:flux:spec:name:tag"
	// TODO: Use a proper URN parser
	var name, tag string
	// Simple split by last colon
	for i := len(urn) - 1; i >= 0; i-- {
		if urn[i] == ':' {
			name = urn[:i]
			tag = urn[i+1:]
			break
		}
	}

	if name == "" || tag == "" {
		return nil, fmt.Errorf("invalid URN format: %s (expected name:tag or hash)", urn)
	}

	// 3. Resolve Name/Tag to Hash
	specs, ok := m.index.Specs[name]
	if !ok {
		// Try scenarios
		specs, ok = m.index.Scenarios[name]
		if !ok {
			return nil, fmt.Errorf("artifact not found: %s", name)
		}
	}

	hash, ok := specs[tag]
	if !ok {
		if tag == "latest" {
			tag = m.resolveLatest(specs)
			if tag == "" {
				return nil, fmt.Errorf("no versions found for: %s", name)
			}
			hash = specs[tag]
		} else {
			return nil, fmt.Errorf("version not found: %s:%s", name, tag)
		}
	}

	// 4. Retrieve from Store
	return m.store.Get(hash)
}

func (m *DefaultManager) List(ctx context.Context) ([]ArtifactInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var artifacts []ArtifactInfo

	for name, tags := range m.index.Specs {
		for tag, hash := range tags {
			if tag == "latest" {
				continue // Skip convenience tag
			}
			artifacts = append(artifacts, ArtifactInfo{
				Name: name,
				Tag:  tag,
				Hash: hash,
			})
		}
	}

	// Add Scenarios... (Parity)
	for name, tags := range m.index.Scenarios {
		for tag, hash := range tags {
			if tag == "latest" {
				continue
			}
			artifacts = append(artifacts, ArtifactInfo{
				Name: name,
				Tag:  tag,
				Hash: hash,
			})
		}
	}

	return artifacts, nil
}

// resolveLatest returns the highest semver tag name from a tag map.
func (m *DefaultManager) resolveLatest(tags map[string]string) string {
	var versions []string
	for t := range tags {
		if semver.IsValid(t) {
			versions = append(versions, t)
		}
	}
	if len(versions) == 0 {
		return ""
	}
	semver.Sort(versions)
	return versions[len(versions)-1]
}

// resolveLatestHash returns the hash of the highest semver tag.
// Used to keep the "latest" index entry in sync with semver ordering
// rather than relying on import order.
func (m *DefaultManager) resolveLatestHash(tags map[string]string) string {
	tag := m.resolveLatest(tags)
	if tag == "" {
		return ""
	}
	return tags[tag]
}
