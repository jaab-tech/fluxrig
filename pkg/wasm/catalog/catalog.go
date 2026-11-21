// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package catalog provides Wasm Supply Chain Security, Validation, and Storage.
package catalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// The buckets the catalog keeps on the bus, shared by every Mixer: the signed
// modules, which the Racks fetch, and the trusted vendor keys.
const (
	CatalogBucket = "wasm_catalog"
	KeysBucket    = "wasm_keys"
)

// revokedKey is the value a revoked vendor keeps in the keyring, so a Mixer
// that still has the key in its keys directory does not add it back.
const revokedKey = "revoked"

// ErrUnavailable reports that the catalog could not reach its store on the
// bus. Nothing was imported, and the request can be tried again.
var ErrUnavailable = errors.New("wasm catalog: the store on the bus is not available")

// ErrInvalidKey reports a vendor name or a public key the keyring refuses.
var ErrInvalidKey = errors.New("wasm catalog: invalid trusted key")

var vendorName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// WasmMetadata holds information about an imported Wasm module.
type WasmMetadata struct {
	Hash      string `json:"hash"`
	SizeBytes int    `json:"size_bytes"`
	Status    string `json:"status"` // "Trusted: VendorX" or "Untrusted"
}

// TrustedKey describes one vendor of the keyring.
type TrustedKey struct {
	Vendor string `json:"vendor"`
	// Fingerprint is the SHA-256 of the public key, in hex.
	Fingerprint string `json:"fingerprint,omitempty"`
	Revoked     bool   `json:"revoked"`
}

// Store holds the catalog and the keyring on the bus, where every Mixer and
// every Rack reads them. Get returns a nil value for a key that does not
// exist.
type Store interface {
	Put(ctx context.Context, bucket, key string, value []byte) (revision uint64, err error)
	Get(ctx context.Context, bucket, key string) (value []byte, revision uint64, err error)
	Keys(ctx context.Context, bucket string) ([]string, error)
}

// CatalogManager handles the validation, security signing, and storage of Wasm modules.
//
// With a store, the catalog and the keyring live on the bus, so every Mixer
// lists the same modules and trusts the same vendors. The catalog directory
// keeps a local copy of each module this Mixer imports. Without a store, the
// directories are the catalog and the keyring.
type CatalogManager struct {
	log        *slog.Logger
	catalogDir string
	keysDir    string
	clusterKey *pki.ClusterKey
	store      Store
	// localKeys is the keyring without a store, read from keysDir.
	localKeys map[string]ed25519.PublicKey
	mu        sync.RWMutex
}

// NewCatalogManager initializes the Wasm Catalog.
func NewCatalogManager(log *slog.Logger, catalogDir, keysDir string, clusterKey *pki.ClusterKey, store Store) (*CatalogManager, error) {
	if err := os.MkdirAll(catalogDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create wasm catalog dir: %w", err)
	}
	if err := os.MkdirAll(keysDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create wasm keys dir: %w", err)
	}

	cm := &CatalogManager{
		log:        log.With("flux.type", "WASM", "flux.name", "catalog"),
		catalogDir: catalogDir,
		keysDir:    keysDir,
		clusterKey: clusterKey,
		store:      store,
	}
	if store == nil {
		keys, err := cm.readKeyFiles()
		if err != nil {
			cm.log.Warn("failed to load initial keyring", "error", err)
		}
		cm.localKeys = keys
	}
	return cm, nil
}

// readKeyFiles reads the .pub files of the keys directory: one Ed25519 public
// key each, named after its vendor.
func (cm *CatalogManager) readKeyFiles() (map[string]ed25519.PublicKey, error) {
	keys := make(map[string]ed25519.PublicKey)
	entries, err := os.ReadDir(cm.keysDir)
	if err != nil {
		return keys, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		vendor := strings.TrimSuffix(entry.Name(), ".pub")
		data, err := os.ReadFile(filepath.Join(cm.keysDir, entry.Name()))
		if err != nil {
			cm.log.Warn("failed to read key file", "file", entry.Name(), "error", err)
			continue
		}
		if len(data) != ed25519.PublicKeySize || !vendorName.MatchString(vendor) {
			cm.log.Warn("invalid trusted key file: a vendor name of letters, digits, - and _, and a 32-byte key", "file", entry.Name())
			continue
		}
		keys[vendor] = ed25519.PublicKey(data)
	}
	return keys, nil
}

// SeedKeyring adds to the shared keyring each key of this Mixer's keys
// directory that it does not hold. The shared keyring wins: a key it holds
// with other bytes, or holds as revoked, is not changed, and the file is
// reported. Without a store it does nothing.
func (cm *CatalogManager) SeedKeyring(ctx context.Context) error {
	if cm.store == nil {
		return nil
	}
	files, err := cm.readKeyFiles()
	if err != nil {
		return err
	}
	for vendor, pub := range files {
		cur, _, err := cm.store.Get(ctx, KeysBucket, vendor)
		if err != nil {
			return fmt.Errorf("%w: read the keyring: %w", ErrUnavailable, err)
		}
		switch {
		case cur == nil:
			if _, err = cm.store.Put(ctx, KeysBucket, vendor, pub); err != nil {
				return fmt.Errorf("%w: add a key to the keyring: %w", ErrUnavailable, err)
			}
			cm.log.Info("trusted key added to the shared keyring from the keys directory", "vendor", vendor)
		case string(cur) == revokedKey:
			cm.log.Warn("trusted key file of a revoked vendor is ignored; remove the file", "vendor", vendor, "dir", cm.keysDir)
		case !bytes.Equal(cur, pub):
			cm.log.Warn("trusted key file differs from the shared keyring, which wins", "vendor", vendor, "dir", cm.keysDir)
		}
	}
	return nil
}

// keyring returns the vendors trusted now.
func (cm *CatalogManager) keyring(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	if cm.store == nil {
		cm.mu.RLock()
		defer cm.mu.RUnlock()
		return cm.localKeys, nil
	}
	vendors, err := cm.store.Keys(ctx, KeysBucket)
	if err != nil {
		return nil, fmt.Errorf("%w: list the keyring: %w", ErrUnavailable, err)
	}
	keys := make(map[string]ed25519.PublicKey, len(vendors))
	for _, vendor := range vendors {
		pub, _, err := cm.store.Get(ctx, KeysBucket, vendor)
		if err != nil {
			return nil, fmt.Errorf("%w: read the keyring: %w", ErrUnavailable, err)
		}
		if len(pub) == ed25519.PublicKeySize {
			keys[vendor] = ed25519.PublicKey(pub)
		}
	}
	return keys, nil
}

// ListKeys returns every vendor of the keyring, revoked ones included.
func (cm *CatalogManager) ListKeys(ctx context.Context) ([]TrustedKey, error) {
	if cm.store == nil {
		cm.mu.RLock()
		defer cm.mu.RUnlock()
		out := make([]TrustedKey, 0, len(cm.localKeys))
		for vendor, pub := range cm.localKeys {
			out = append(out, TrustedKey{Vendor: vendor, Fingerprint: fingerprint(pub)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Vendor < out[j].Vendor })
		return out, nil
	}
	vendors, err := cm.store.Keys(ctx, KeysBucket)
	if err != nil {
		return nil, fmt.Errorf("%w: list the keyring: %w", ErrUnavailable, err)
	}
	sort.Strings(vendors)
	out := make([]TrustedKey, 0, len(vendors))
	for _, vendor := range vendors {
		pub, _, err := cm.store.Get(ctx, KeysBucket, vendor)
		if err != nil {
			return nil, fmt.Errorf("%w: read the keyring: %w", ErrUnavailable, err)
		}
		k := TrustedKey{Vendor: vendor, Revoked: string(pub) == revokedKey}
		if !k.Revoked {
			k.Fingerprint = fingerprint(pub)
		}
		out = append(out, k)
	}
	return out, nil
}

// TrustKey adds a vendor's Ed25519 public key to the shared keyring, or
// replaces it, a revoked one included. Every Mixer trusts it at its next
// import.
func (cm *CatalogManager) TrustKey(ctx context.Context, vendor string, pub []byte) error {
	if !vendorName.MatchString(vendor) {
		return fmt.Errorf("%w: a vendor name is 1 to 64 letters, digits, - and _", ErrInvalidKey)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: an Ed25519 public key is %d bytes, got %d", ErrInvalidKey, ed25519.PublicKeySize, len(pub))
	}
	if cm.store == nil {
		return fmt.Errorf("%w: without a store, trusted keys are files in %s", ErrUnavailable, cm.keysDir)
	}
	if _, err := cm.store.Put(ctx, KeysBucket, vendor, pub); err != nil {
		return fmt.Errorf("%w: add a key to the keyring: %w", ErrUnavailable, err)
	}
	cm.log.Info("trusted key added to the shared keyring", "vendor", vendor, "fingerprint", fingerprint(pub))
	return nil
}

// RevokeKey stops trusting a vendor on every Mixer. The vendor stays in the
// keyring as revoked, so a Mixer whose keys directory still holds its file
// does not add it back.
func (cm *CatalogManager) RevokeKey(ctx context.Context, vendor string) error {
	if !vendorName.MatchString(vendor) {
		return fmt.Errorf("%w: a vendor name is 1 to 64 letters, digits, - and _", ErrInvalidKey)
	}
	if cm.store == nil {
		return fmt.Errorf("%w: without a store, trusted keys are files in %s", ErrUnavailable, cm.keysDir)
	}
	if _, err := cm.store.Put(ctx, KeysBucket, vendor, []byte(revokedKey)); err != nil {
		return fmt.Errorf("%w: revoke a key: %w", ErrUnavailable, err)
	}
	cm.log.Info("trusted key revoked in the shared keyring", "vendor", vendor)
	return nil
}

func fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// vendorStatus checks the vendor signature of a module against the keyring.
func vendorStatus(payload []byte, keyring map[string]ed25519.PublicKey) string {
	sig, stripped, err := ExtractCustomSection(payload, "fluxrig.signature")
	if err != nil {
		return "Untrusted"
	}
	sum := sha256.Sum256(stripped)
	for vendor, pub := range keyring {
		if ed25519.Verify(pub, sum[:], sig) {
			return "Trusted: " + vendor
		}
	}
	return "Untrusted"
}

// List returns metadata for all installed modules. With a store it lists the
// shared catalog, the same on every Mixer; without one, this Mixer's catalog
// directory.
func (cm *CatalogManager) List(ctx context.Context) ([]WasmMetadata, error) {
	keyring, err := cm.keyring(ctx)
	if err != nil {
		return nil, err
	}
	if cm.store == nil {
		return cm.listDir(keyring)
	}
	names, err := cm.store.Keys(ctx, CatalogBucket)
	if err != nil {
		return nil, fmt.Errorf("%w: list the catalog: %w", ErrUnavailable, err)
	}
	sort.Strings(names)
	list := make([]WasmMetadata, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".wasm") {
			continue
		}
		data, _, err := cm.store.Get(ctx, CatalogBucket, name)
		if err != nil {
			return nil, fmt.Errorf("%w: read the catalog: %w", ErrUnavailable, err)
		}
		if data == nil {
			continue // deleted between the listing and the read
		}
		list = append(list, metadataOf(name, data, keyring))
	}
	return list, nil
}

func (cm *CatalogManager) listDir(keyring map[string]ed25519.PublicKey) ([]WasmMetadata, error) {
	entries, err := os.ReadDir(cm.catalogDir)
	if err != nil {
		return nil, err
	}
	var list []WasmMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wasm") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cm.catalogDir, entry.Name()))
		if err != nil {
			continue
		}
		list = append(list, metadataOf(entry.Name(), data, keyring))
	}
	return list, nil
}

// metadataOf describes a stored module: the cluster signature, which every
// imported module carries, is taken off before the vendor signature is checked.
func metadataOf(name string, data []byte, keyring map[string]ed25519.PublicKey) WasmMetadata {
	status := "Unknown"
	if _, unsigned, err := ExtractCustomSection(data, "fluxrig.cluster.signature"); err == nil {
		status = vendorStatus(unsigned, keyring)
	}
	return WasmMetadata{Hash: strings.TrimSuffix(name, ".wasm"), SizeBytes: len(data), Status: status}
}

// Import validates and securely imports a Wasm binary. It returns
// ErrUnavailable, and imports nothing, when the store does not take it.
func (cm *CatalogManager) Import(ctx context.Context, payload []byte, allowUnsigned bool) (WasmMetadata, error) {
	keyring, err := cm.keyring(ctx)
	if err != nil {
		return WasmMetadata{}, err
	}

	// 1. Extract Vendor Signature (if present)
	vendorSig, strippedPayload, errExt := ExtractCustomSection(payload, "fluxrig.signature")
	hasVendorSig := errExt == nil
	var trustedVendor string
	isTrusted := false

	if hasVendorSig {
		// Vendor signature found. Verify against Keyring.
		hashBytes := sha256.Sum256(strippedPayload)
		for vendor, pub := range keyring {
			if ed25519.Verify(pub, hashBytes[:], vendorSig) {
				isTrusted = true
				trustedVendor = vendor
				break
			}
		}

		if !isTrusted && !allowUnsigned {
			return WasmMetadata{}, fmt.Errorf("vendor signature found but does not match any trusted keys")
		}
	} else if !allowUnsigned {
		return WasmMetadata{}, fmt.Errorf("missing vendor signature (use allow_unsigned to bypass)")
	}

	// Work with the stripped payload from now on
	if strippedPayload == nil {
		strippedPayload = payload
	}

	// 2. Pre-flight Security Validation (Sandboxing Check)
	if err = cm.validateSandbox(ctx, strippedPayload); err != nil {
		return WasmMetadata{}, fmt.Errorf("sandbox validation failed: %w", err)
	}

	// 3. Hash the original payload so the Rack can verify it (it includes the vendor signature if present)
	hashBytes := sha256.Sum256(payload)
	hashStr := hex.EncodeToString(hashBytes[:])

	// 4. Cluster Signature
	if cm.clusterKey == nil {
		return WasmMetadata{}, fmt.Errorf("mixer cluster key is required to sign imported wasm payloads")
	}

	clusterSig, err := cm.clusterKey.SignBytes(hashBytes[:])
	if err != nil {
		return WasmMetadata{}, fmt.Errorf("failed to sign wasm payload: %w", err)
	}

	// 5. Append the cluster signature to the original payload, so the vendor
	// signature stays in it.
	signedPayload, err := AppendCustomSection(payload, "fluxrig.cluster.signature", clusterSig)
	if err != nil {
		return WasmMetadata{}, fmt.Errorf("failed to append cluster signature: %w", err)
	}

	// 6. Distribute: the store is where the Racks fetch the module, so an
	// import it does not take is not an import.
	if cm.store != nil {
		if _, err = cm.store.Put(ctx, CatalogBucket, hashStr+".wasm", signedPayload); err != nil {
			return WasmMetadata{}, fmt.Errorf("%w: store the module %s: %w", ErrUnavailable, hashStr, err)
		}
	}

	// 7. The local copy. Without a store it is the catalog.
	savePath := filepath.Join(cm.catalogDir, hashStr+".wasm")
	if err = os.WriteFile(savePath, signedPayload, 0600); err != nil {
		if cm.store == nil {
			return WasmMetadata{}, fmt.Errorf("failed to write wasm file: %w", err)
		}
		cm.log.Warn("failed to keep the local copy of an imported module", "hash", hashStr, "error", err)
	}

	status := "Untrusted"
	if isTrusted {
		status = "Trusted: " + trustedVendor
	}

	cm.log.Info("wasm module imported", "hash", hashStr, "status", status)

	return WasmMetadata{
		Hash:      hashStr,
		SizeBytes: len(signedPayload),
		Status:    status,
	}, nil
}

// HashWasm calculates the SHA-256 hash of a Wasm binary.
func HashWasm(payload []byte) []byte {
	h := sha256.Sum256(payload)
	return h[:]
}

var SkipSandboxValidationForTests = false

// validateSandbox uses wazero to parse the Wasm module and enforce import restrictions.
func (cm *CatalogManager) validateSandbox(ctx context.Context, payload []byte) error {
	if SkipSandboxValidationForTests {
		return nil
	}

	rt := wazero.NewRuntime(ctx)
	defer func() { _ = rt.Close(ctx) }()

	compiled, err := rt.CompileModule(ctx, payload)
	if err != nil {
		return fmt.Errorf("compile failed: %w", err)
	}

	allowedModules := map[string]bool{
		"env": true,
	}

	allowedFunctions := map[string]bool{
		"env.log": true,
	}

	for _, imp := range compiled.ImportedFunctions() {
		mod, name, _ := imp.Import()
		if !allowedModules[mod] {
			return fmt.Errorf("unauthorized module import: %s (only 'env' is allowed)", mod)
		}
		fqn := mod + "." + name
		if !allowedFunctions[fqn] {
			return fmt.Errorf("unauthorized function import: %s (only env.log is allowed)", fqn)
		}
	}

	// Ensure required exports exist
	exports := compiled.ExportedFunctions()
	if _, ok := exports["process"]; !ok {
		return fmt.Errorf("missing required export: 'process'")
	}
	if _, ok1 := exports["alloc"]; !ok1 {
		if _, ok2 := exports["malloc"]; !ok2 {
			return fmt.Errorf("missing required export: 'alloc' or 'malloc'")
		}
	}
	if _, ok := exports["free"]; !ok {
		return fmt.Errorf("missing required export: 'free'")
	}

	return nil
}
