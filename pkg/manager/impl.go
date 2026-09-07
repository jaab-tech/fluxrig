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
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
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

	// Attrs is what the store knows about each version beyond its hash, keyed by
	// "<kind>/<name>:<tag>". It is a separate map rather than a richer value in
	// the maps above so that an index written before this existed still loads:
	// those entries simply have no attributes, which the listing reports as
	// unknown instead of inventing a date from a file timestamp.
	Attrs map[string]ArtifactAttrs `json:"attrs,omitempty"`
}

// ArtifactAttrs is what was recorded at import time.
type ArtifactAttrs struct {
	ImportedAt time.Time `json:"imported_at"`
	Size       int64     `json:"size"`
	Title      string    `json:"title,omitempty"`
	Protocol   string    `json:"protocol,omitempty"`
}

// attrKey addresses one version's attributes.
func attrKey(kind Kind, name, tag string) string {
	return string(kind) + "/" + name + ":" + tag
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

// SpecHeader is the name and version a document declares about itself.
//
// The two kinds put them in different places and neither at the top level, which
// is where this used to look: a spec carries them under `spec:`, a scenario under
// `meta:`. Reading nothing meant every import fell through to an auto-incremented
// tag, so a spec declaring 2.2.0 was stored as v0.1.0 and the same file imported
// twice became two versions of itself. The immutability check below could never
// fire, because no two imports ever landed on the same tag.
type SpecHeader struct {
	Name    string
	Version string
	// Title is the human name, which is not the reference. It is kept so a
	// listing can show both: "iso8583-v87-ascii" is what a scenario writes and
	// "ISO 8583:1987 (ASCII)" is what a person recognises.
	Title    string
	Protocol string
	Meta     map[string]interface{}
}

// parseHeader reads the declared identity from either document shape. A
// top-level pair is still honoured last, so a document that predates the two
// shapes is not suddenly nameless.
func parseHeader(content []byte) SpecHeader {
	var doc struct {
		Spec struct {
			// `id` is the slug a reference is written with; `name` is the human
			// title and may carry spaces, parentheses and colons — the reference
			// spec's is "ISO 8583:1987 (ASCII)", which is not something to put in
			// a name:tag. Prefer the id, fall back to the title.
			ID       string `yaml:"id"`
			Name     string `yaml:"name"`
			Version  string `yaml:"version"`
			Protocol string `yaml:"protocol"`
		} `yaml:"spec"`
		Meta struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"meta"`
		Name     string                 `yaml:"name"`
		Version  string                 `yaml:"version"`
		Metadata map[string]interface{} `yaml:"metadata,omitempty"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return SpecHeader{}
	}
	h := SpecHeader{Meta: doc.Metadata, Title: doc.Spec.Name, Protocol: doc.Spec.Protocol}
	for _, c := range []struct{ name, version string }{
		{doc.Spec.ID, doc.Spec.Version},
		{doc.Spec.Name, doc.Spec.Version},
		{doc.Meta.Name, doc.Meta.Version},
		{doc.Name, doc.Version},
	} {
		if h.Name == "" {
			h.Name = c.name
		}
		if h.Version == "" {
			h.Version = c.version
		}
	}
	return h
}

// semverTag normalises a declared version into the tag vocabulary. Documents
// declare `2.2.0`, the way a version is written everywhere else; the CAS orders
// tags with golang.org/x/mod/semver, which requires the leading v.
func semverTag(v string) string {
	if v == "" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// Import snapshots a local spec file into the CAS.
func (m *DefaultManager) Import(ctx context.Context, filePath, nameOverride, tagOverride string) (string, string, string, error) {
	content, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read file: %w", err)
	}
	return m.importArtifact(content, fallbackName(filePath), nameOverride, tagOverride, m.index.Specs, "spec")
}

// ImportContent stores a spec that arrived as bytes. A document pushed to a Rack
// has no filename to fall back on, so it must name itself.
func (m *DefaultManager) ImportContent(ctx context.Context, content []byte, nameOverride, tagOverride string) (string, string, string, error) {
	return m.importArtifact(content, "", nameOverride, tagOverride, m.index.Specs, "spec")
}

// fallbackName is the last resort for a document that declares no name: what the
// file was called.
func fallbackName(filePath string) string {
	base := filepath.Base(filePath)
	return base[:len(base)-len(filepath.Ext(base))]
}

// ImportScenario snapshots a local scenario file into the CAS.
func (m *DefaultManager) ImportScenario(ctx context.Context, filePath, nameOverride, tagOverride string) (string, string, string, error) {
	content, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read file: %w", err)
	}
	return m.importArtifact(content, fallbackName(filePath), nameOverride, tagOverride, m.index.Scenarios, "scenario")
}

// importArtifact is the shared import logic for specs and scenarios.
func (m *DefaultManager) importArtifact(content []byte, nameFallback, nameOverride, tagOverride string, index map[string]map[string]string, kind string) (string, string, string, error) {
	// 1. What the document says it is
	header := parseHeader(content)

	// 2. Determine Name
	finalName := nameOverride
	if finalName == "" {
		finalName = header.Name
	}
	if finalName == "" {
		finalName = nameFallback
	}
	if finalName == "" {
		return "", "", "", fmt.Errorf("%s declares no name and none was given", kind)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 3. Determine Tag (Version)
	finalTag := semverTag(tagOverride)
	if finalTag == "" {
		finalTag = semverTag(header.Version)
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

	// What the store knows about this version, recorded once at import. A
	// re-import of identical bytes keeps the original date: the artefact was
	// filed then, and the second import created nothing.
	if m.index.Attrs == nil {
		m.index.Attrs = make(map[string]ArtifactAttrs)
	}
	key := attrKey(Kind(kind), finalName, finalTag)
	if _, already := m.index.Attrs[key]; !already {
		m.index.Attrs[key] = ArtifactAttrs{
			ImportedAt: time.Now().UTC(),
			Size:       int64(len(content)),
			Title:      header.Title,
			Protocol:   header.Protocol,
		}
	}

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
	artifacts = append(artifacts, m.collect(KindSpec, m.index.Specs)...)
	artifacts = append(artifacts, m.collect(KindScenario, m.index.Scenarios)...)
	sortArtifacts(artifacts)
	return artifacts, nil
}

// History returns every version of one artifact, newest version first.
func (m *DefaultManager) History(ctx context.Context, kind Kind, name string) ([]ArtifactInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	index := m.index.Specs
	if kind == KindScenario {
		index = m.index.Scenarios
	}
	tags, ok := index[name]
	if !ok {
		return nil, fmt.Errorf("no %s named %q in the store", kind, name)
	}
	out := m.collect(kind, map[string]map[string]string{name: tags})
	// Newest first, and newest means the highest version -- not the last
	// imported. A patch to an old branch arrives after a newer release and is
	// not newer than it.
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(out[i].Tag, out[j].Tag) > 0
	})
	return out, nil
}

// collect turns one kind's index into listings, attaching what the store
// recorded. It must be called with the lock held.
func (m *DefaultManager) collect(kind Kind, index map[string]map[string]string) []ArtifactInfo {
	var out []ArtifactInfo
	for name, tags := range index {
		latest := tags["latest"]
		for tag, hash := range tags {
			if tag == "latest" {
				continue // a convenience alias, not a version
			}
			info := ArtifactInfo{
				Kind: kind,
				Name: name,
				Tag:  tag,
				Hash: hash,
				// The alias points at a hash, so several tags carrying the same
				// bytes are all "latest" -- which is true, and is what a reader
				// needs to know about any of them.
				Latest: latest != "" && latest == hash,
			}
			if a, ok := m.index.Attrs[attrKey(kind, name, tag)]; ok {
				info.ImportedAt, info.Size = a.ImportedAt, a.Size
				info.Title, info.Protocol = a.Title, a.Protocol
			}
			out = append(out, info)
		}
	}
	return out
}

// sortArtifacts gives a listing a stable order: by kind, then name, then
// version descending, so the newest of each artifact reads first.
func sortArtifacts(a []ArtifactInfo) {
	sort.Slice(a, func(i, j int) bool {
		if a[i].Kind != a[j].Kind {
			return a[i].Kind < a[j].Kind
		}
		if a[i].Name != a[j].Name {
			return a[i].Name < a[j].Name
		}
		return semver.Compare(a[i].Tag, a[j].Tag) > 0
	})
}
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
