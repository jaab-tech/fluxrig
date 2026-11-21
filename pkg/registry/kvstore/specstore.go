// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// SpecStore is the spec store every Mixer of a cluster shares. It lives in one
// JetStream key-value bucket:
//
//	blob.<sha256 hex>             the spec's bytes; the hash is bare hex, as in the local store
//	spec.<name b64>.<tag b64>     a specVersion: the hash and what the import recorded
//	meta.migrated                 set once the lease holder copied its local store
//
// Name and tag are base64url-encoded because a key allows few characters and
// a tag has dots. "latest" is not stored: it is the highest version, computed
// on read, so it cannot go stale.
//
// Blobs are values, not Object Store objects: an object read goes through an
// ordered consumer, which a cluster with a member cut off can place on that
// member, where it never starts. A value read goes through Bucket.Get, which
// asks the stream leader. The cost is a size cap, the bucket's max value size.
//
// Every write needs the write lease, like any registry write, so one writer
// allocates versions. A version is a Create: it fails for every Mixer of the
// cluster if the version already names other bytes.
type SpecStore struct {
	bucket  Bucket
	maxSize int
	lease   *Lease
	now     func() time.Time
}

// specVersion is the value of a spec.<name>.<tag> key.
type specVersion struct {
	Hash       string    `json:"hash"`
	ImportedAt time.Time `json:"imported_at"`
	Size       int64     `json:"size"`
	Title      string    `json:"title,omitempty"`
	Protocol   string    `json:"protocol,omitempty"`
}

const (
	specBlobPrefix    = "blob."
	specVersionPrefix = "spec."
	specMigratedKey   = "meta.migrated"
	hashPrefix        = "sha256:"
)

var _ manager.Manager = (*SpecStore)(nil)

// NewSpecStore returns a store over bucket. maxSize is the largest spec it
// accepts, which must not exceed the bucket's max value size. lease is the
// write lease; nil means this process may always write (a test, or a store
// used without the lease).
func NewSpecStore(bucket Bucket, maxSize int, lease *Lease) *SpecStore {
	return &SpecStore{bucket: bucket, maxSize: maxSize, lease: lease, now: time.Now}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func specVersionKey(name, tag string) string {
	return specVersionPrefix + b64(name) + "." + b64(tag)
}

// parseVersionKey decodes a spec.<name>.<tag> key.
func parseVersionKey(key string) (name, tag string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(key, specVersionPrefix), ".")
	if len(parts) != 2 {
		return "", "", false
	}
	n, errN := base64.RawURLEncoding.DecodeString(parts[0])
	t, errT := base64.RawURLEncoding.DecodeString(parts[1])
	if errN != nil || errT != nil {
		return "", "", false
	}
	return string(n), string(t), true
}

// isHash reports whether s is a sha256 in hex, the form the stores use.
func isHash(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func blobKey(hash string) (string, error) {
	h := strings.TrimPrefix(hash, hashPrefix)
	if len(h) != sha256.Size*2 {
		return "", fmt.Errorf("kvstore: specs: %q is not a sha256 hash", hash)
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("kvstore: specs: %q is not a sha256 hash", hash)
	}
	return specBlobPrefix + h, nil
}

func (s *SpecStore) requireLease() error {
	if s.lease != nil && !s.lease.Held() {
		return registry.ErrNotLeaseHolder
	}
	return nil
}

// Import stores a spec file. A document that declares no name takes the file
// name.
func (s *SpecStore) Import(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	content, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return "", "", "", fmt.Errorf("kvstore: specs: read %s: %w", filePath, err)
	}
	if name == "" && manager.ParseHeader(content).Name == "" {
		base := filepath.Base(filePath)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return s.ImportContent(ctx, content, name, tag)
}

// ImportContent stores a spec. The name and tag come from the arguments, then
// from what the document declares; a document with no version gets the minor
// after the highest existing one. Importing the same bytes under the same
// version again succeeds and changes nothing.
func (s *SpecStore) ImportContent(ctx context.Context, content []byte, name, tag string) (string, string, string, error) {
	header := manager.ParseHeader(content)
	return s.importContent(ctx, content, name, tag, specVersion{
		ImportedAt: s.now().UTC(),
		Title:      header.Title,
		Protocol:   header.Protocol,
	})
}

// importContent stores content with the attributes in attrs. Hash and Size
// are filled here.
func (s *SpecStore) importContent(ctx context.Context, content []byte, name, tag string, attrs specVersion) (string, string, string, error) {
	if err := s.requireLease(); err != nil {
		return "", "", "", err
	}
	if s.maxSize > 0 && len(content) > s.maxSize {
		return "", "", "", fmt.Errorf("%w: %d bytes, the limit is %d (registry.spec_max_size)", manager.ErrTooLarge, len(content), s.maxSize)
	}
	header := manager.ParseHeader(content)
	if name == "" {
		name = header.Name
	}
	if name == "" {
		return "", "", "", fmt.Errorf("kvstore: specs: the spec declares no name and none was given")
	}
	tag = manager.SemverTag(tag)
	if tag == "" {
		tag = manager.SemverTag(header.Version)
	}
	if tag == "" {
		existing, err := s.versions(ctx, name)
		if err != nil {
			return "", "", "", err
		}
		tags := make([]string, 0, len(existing))
		for t := range existing {
			tags = append(tags, t)
		}
		tag = manager.NextMinor(tags)
	}
	if !semver.IsValid(tag) || tag == "latest" {
		return "", "", "", fmt.Errorf("kvstore: specs: invalid version %q: it must start with v and follow semver", tag)
	}

	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:]) // the local store's form: bare hex
	key, _ := blobKey(hash)
	// Content-addressed: the same bytes always land under one key, so a
	// conflict means they are already there.
	if _, err := s.bucket.Create(ctx, key, content); err != nil && !errors.Is(err, errConflict) {
		return "", "", "", fmt.Errorf("kvstore: specs: store %s: %w", hash, err)
	}

	attrs.Hash, attrs.Size = hash, int64(len(content))
	value, err := json.Marshal(attrs)
	if err != nil {
		return "", "", "", err
	}
	vkey := specVersionKey(name, tag)
	if _, err := s.bucket.Create(ctx, vkey, value); err != nil {
		if !errors.Is(err, errConflict) {
			return "", "", "", fmt.Errorf("kvstore: specs: record %s:%s: %w", name, tag, err)
		}
		cur, errGet := s.version(ctx, name, tag)
		if errGet != nil {
			return "", "", "", errGet
		}
		if cur.Hash != hash {
			return "", "", "", fmt.Errorf("%w: %s:%s, please increment the version", manager.ErrVersionExists, name, tag)
		}
	}
	return hash, name, tag, nil
}

// ImportScenario is not supported: on a Mixer, scenarios live in the registry.
func (s *SpecStore) ImportScenario(context.Context, string, string, string) (string, string, string, error) {
	return "", "", "", fmt.Errorf("kvstore: specs: scenarios are stored in the registry, not in the spec store")
}

// version reads one version record.
func (s *SpecStore) version(ctx context.Context, name, tag string) (specVersion, error) {
	raw, _, err := s.bucket.Get(ctx, specVersionKey(name, tag))
	if errors.Is(err, errKeyNotFound) {
		return specVersion{}, fmt.Errorf("version not found: %s:%s: %w", name, tag, registry.ErrNotFound)
	}
	if err != nil {
		return specVersion{}, fmt.Errorf("kvstore: specs: read %s:%s: %w", name, tag, err)
	}
	var v specVersion
	if err := json.Unmarshal(raw, &v); err != nil {
		return specVersion{}, fmt.Errorf("kvstore: specs: decode %s:%s: %w", name, tag, err)
	}
	return v, nil
}

// versions reads every version of one spec, by tag.
func (s *SpecStore) versions(ctx context.Context, name string) (map[string]specVersion, error) {
	keys, err := s.bucket.Keys(ctx, specVersionPrefix+b64(name)+".")
	if err != nil {
		return nil, fmt.Errorf("kvstore: specs: list %s: %w", name, err)
	}
	out := make(map[string]specVersion, len(keys))
	for _, k := range keys {
		n, tag, ok := parseVersionKey(k)
		if !ok || n != name {
			continue
		}
		v, errV := s.version(ctx, name, tag)
		if errors.Is(errV, registry.ErrNotFound) {
			continue // removed since the listing
		}
		if errV != nil {
			return nil, errV
		}
		out[tag] = v
	}
	return out, nil
}

// Load returns a spec's bytes, by hash (bare hex, or with a sha256: prefix),
// name:tag or name:latest.
func (s *SpecStore) Load(ctx context.Context, urn string) ([]byte, error) {
	hash := strings.TrimPrefix(urn, hashPrefix)
	if !isHash(hash) {
		name, tag, err := manager.ParseURN(urn)
		if err != nil {
			return nil, err
		}
		if tag == "latest" {
			vs, errV := s.versions(ctx, name)
			if errV != nil {
				return nil, errV
			}
			tags := make([]string, 0, len(vs))
			for t := range vs {
				tags = append(tags, t)
			}
			tag = manager.LatestTag(tags)
			if tag == "" {
				return nil, fmt.Errorf("no versions found for: %s: %w", name, registry.ErrNotFound)
			}
			hash = vs[tag].Hash
		} else {
			v, errV := s.version(ctx, name, tag)
			if errV != nil {
				return nil, errV
			}
			hash = v.Hash
		}
	}
	key, err := blobKey(hash)
	if err != nil {
		return nil, err
	}
	content, _, err := s.bucket.Get(ctx, key)
	if errors.Is(err, errKeyNotFound) {
		return nil, fmt.Errorf("artifact not found: %s: %w", urn, registry.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("kvstore: specs: read %s: %w", hash, err)
	}
	return content, nil
}

// Export writes a spec's bytes to outputPath.
func (s *SpecStore) Export(ctx context.Context, urn, outputPath string) error {
	content, err := s.Load(ctx, urn)
	if err != nil {
		return err
	}
	clean := filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
		return err
	}
	return os.WriteFile(clean, content, 0o600)
}

// List returns every version of every spec.
func (s *SpecStore) List(ctx context.Context) ([]manager.ArtifactInfo, error) {
	keys, err := s.bucket.Keys(ctx, specVersionPrefix)
	if err != nil {
		return nil, fmt.Errorf("kvstore: specs: list: %w", err)
	}
	byName := map[string]map[string]specVersion{}
	for _, k := range keys {
		name, tag, ok := parseVersionKey(k)
		if !ok {
			continue
		}
		v, errV := s.version(ctx, name, tag)
		if errors.Is(errV, registry.ErrNotFound) {
			continue
		}
		if errV != nil {
			return nil, errV
		}
		if byName[name] == nil {
			byName[name] = map[string]specVersion{}
		}
		byName[name][tag] = v
	}
	var out []manager.ArtifactInfo
	for name, vs := range byName {
		out = append(out, artifacts(name, vs)...)
	}
	manager.SortArtifacts(out)
	return out, nil
}

// History returns every version of one spec, newest version first.
func (s *SpecStore) History(ctx context.Context, kind manager.Kind, name string) ([]manager.ArtifactInfo, error) {
	if kind != manager.KindSpec {
		return nil, fmt.Errorf("kvstore: specs: the spec store holds specs, not %s", kind)
	}
	vs, err := s.versions(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("no %s named %q in the store: %w", kind, name, registry.ErrNotFound)
	}
	out := artifacts(name, vs)
	sort.Slice(out, func(i, j int) bool { return semver.Compare(out[i].Tag, out[j].Tag) > 0 })
	return out, nil
}

// artifacts turns one spec's versions into listings. Every tag that carries
// the bytes of the highest version is marked latest, as the local store does.
func artifacts(name string, vs map[string]specVersion) []manager.ArtifactInfo {
	tags := make([]string, 0, len(vs))
	for t := range vs {
		tags = append(tags, t)
	}
	latestHash := ""
	if latest := manager.LatestTag(tags); latest != "" {
		latestHash = vs[latest].Hash
	}
	out := make([]manager.ArtifactInfo, 0, len(vs))
	for tag, v := range vs {
		out = append(out, manager.ArtifactInfo{
			Kind: manager.KindSpec, Name: name, Tag: tag, Hash: v.Hash,
			ImportedAt: v.ImportedAt, Size: v.Size, Title: v.Title, Protocol: v.Protocol,
			Latest: latestHash != "" && v.Hash == latestHash,
		})
	}
	return out
}

// MigrateResult is what one migration copied.
type MigrateResult struct {
	// Copied counts the local versions now in the shared store, including
	// any it already held with the same bytes.
	Copied int
	// Conflicts names each name:tag the shared store already holds with other
	// bytes. Those are skipped, never overwritten.
	Conflicts []string
}

// MigrateFrom copies every spec of a Mixer's local store into the shared one,
// once per cluster. It needs the lease. When the cluster has already migrated
// it returns done=true and copies nothing, so only the first holder's local
// store is used. A version the shared store holds with other bytes is
// reported and skipped. The import date the local store recorded is kept.
func (s *SpecStore) MigrateFrom(ctx context.Context, local manager.Manager, mixerID string, log *slog.Logger) (res MigrateResult, done bool, err error) {
	if errLease := s.requireLease(); errLease != nil {
		return res, false, errLease
	}
	if _, _, errGet := s.bucket.Get(ctx, specMigratedKey); errGet == nil {
		return res, true, nil
	} else if !errors.Is(errGet, errKeyNotFound) {
		return res, false, fmt.Errorf("kvstore: specs: read the migration marker: %w", errGet)
	}
	items, err := local.List(ctx)
	if err != nil {
		return res, false, fmt.Errorf("kvstore: specs: list the local store: %w", err)
	}
	for _, it := range items {
		if it.Kind != manager.KindSpec {
			continue
		}
		content, errLoad := local.Load(ctx, it.Hash)
		if errLoad != nil {
			return res, false, fmt.Errorf("kvstore: specs: read local %s:%s: %w", it.Name, it.Tag, errLoad)
		}
		attrs := specVersion{ImportedAt: it.ImportedAt, Title: it.Title, Protocol: it.Protocol}
		if attrs.ImportedAt.IsZero() {
			attrs.ImportedAt = s.now().UTC()
		}
		_, _, _, errImp := s.importContent(ctx, content, it.Name, it.Tag, attrs)
		switch {
		case errors.Is(errImp, manager.ErrVersionExists), errors.Is(errImp, manager.ErrTooLarge):
			res.Conflicts = append(res.Conflicts, it.Name+":"+it.Tag)
			log.Warn("spec migration: skipped a local version the shared store cannot take",
				"spec", it.Name, "version", it.Tag, "error", errImp)
		case errImp != nil:
			return res, false, errImp
		default:
			// The version is now in the shared store with the local bytes:
			// importContent only succeeds when the shared version is
			// absent (just written) or already holds identical bytes
			// (recognised as present, never overwritten), so a success is
			// always a copy, even when the recorded hashes differ.
			res.Copied++
		}
	}
	marker, _ := json.Marshal(map[string]any{"mixer_id": mixerID, "at": s.now().UTC()})
	if _, err := s.bucket.Create(ctx, specMigratedKey, marker); err != nil && !errors.Is(err, errConflict) {
		return res, false, fmt.Errorf("kvstore: specs: write the migration marker: %w", err)
	}
	return res, false, nil
}

// Migrated reports whether a holder already copied its local store.
func (s *SpecStore) Migrated(ctx context.Context) (bool, error) {
	_, _, err := s.bucket.Get(ctx, specMigratedKey)
	if errors.Is(err, errKeyNotFound) {
		return false, nil
	}
	return err == nil, err
}
