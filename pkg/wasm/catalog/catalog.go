// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package catalog provides Wasm Supply Chain Security, Validation, and Storage.
package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// WasmMetadata holds information about an imported Wasm module.
type WasmMetadata struct {
	Hash      string `json:"hash"`
	SizeBytes int    `json:"size_bytes"`
	Status    string `json:"status"` // "Trusted: VendorX" or "Untrusted"
}

// Store is an interface for distributing the Wasm binary.
type Store interface {
	Put(ctx context.Context, bucket, key string, value []byte) (revision uint64, err error)
}

// CatalogManager handles the validation, security signing, and storage of Wasm modules.
type CatalogManager struct {
	log        *slog.Logger
	catalogDir string
	keysDir    string
	keyring    map[string]ed25519.PublicKey
	clusterKey *pki.ClusterKey
	distStore  Store
	mu         sync.RWMutex
}

// NewCatalogManager initializes the Wasm Catalog.
func NewCatalogManager(log *slog.Logger, catalogDir, keysDir string, clusterKey *pki.ClusterKey, distStore Store) (*CatalogManager, error) {
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
		keyring:    make(map[string]ed25519.PublicKey),
		clusterKey: clusterKey,
		distStore:  distStore,
	}

	if err := cm.ReloadKeyring(); err != nil {
		cm.log.Warn("failed to load initial keyring", "error", err)
	}

	return cm, nil
}

// ReloadKeyring scans the TrustedKeysDir for .pub files and loads them.
func (cm *CatalogManager) ReloadKeyring() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	entries, err := os.ReadDir(cm.keysDir)
	if err != nil {
		return err
	}

	newKeyring := make(map[string]ed25519.PublicKey)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		path := filepath.Join(cm.keysDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			cm.log.Warn("failed to read key file", "file", entry.Name(), "error", err)
			continue
		}

		if len(data) != ed25519.PublicKeySize {
			cm.log.Warn("invalid public key length", "file", entry.Name())
			continue
		}
		key := ed25519.PublicKey(data)

		vendorName := strings.TrimSuffix(entry.Name(), ".pub")
		newKeyring[vendorName] = key
	}

	cm.keyring = newKeyring
	cm.log.Info("keyring reloaded", "keys", len(cm.keyring))
	return nil
}

// List returns metadata for all installed modules.
func (cm *CatalogManager) List() (any, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	entries, err := os.ReadDir(cm.catalogDir)
	if err != nil {
		return nil, err
	}

	var list []WasmMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wasm") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		hash := strings.TrimSuffix(entry.Name(), ".wasm")

		// Status would ideally be tracked in a DB, but for now we extract it from the file
		status := "Unknown"
		path := filepath.Join(cm.catalogDir, entry.Name())
		data, err := os.ReadFile(path)
		if err == nil {
			_, stripped, errExt := ExtractCustomSection(data, "fluxrig.cluster.signature")
			if errExt == nil {
				// We can check if it has a vendor signature
				_, _, errVendor := ExtractCustomSection(stripped, "fluxrig.signature")
				if errVendor == nil {
					status = "Trusted"
				} else {
					status = "Untrusted"
				}
			}
		}

		list = append(list, WasmMetadata{
			Hash:      hash,
			SizeBytes: int(info.Size()),
			Status:    status,
		})
	}
	return list, nil
}

// Import validates and securely imports a Wasm binary.
func (cm *CatalogManager) Import(ctx context.Context, payload []byte, allowUnsigned bool) (any, error) {
	cm.mu.RLock()
	keyring := cm.keyring
	cm.mu.RUnlock()

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
			return nil, fmt.Errorf("vendor signature found but does not match any trusted keys")
		}
	} else if !allowUnsigned {
		return nil, fmt.Errorf("missing vendor signature (use allow_unsigned to bypass)")
	}

	// Work with the stripped payload from now on
	if strippedPayload == nil {
		strippedPayload = payload
	}

	// 2. Pre-flight Security Validation (Sandboxing Check)
	if err := cm.validateSandbox(ctx, strippedPayload); err != nil {
		return nil, fmt.Errorf("sandbox validation failed: %w", err)
	}

	// 3. Hash the original payload so the Rack can verify it (it includes the vendor signature if present)
	hashBytes := sha256.Sum256(payload)
	hashStr := hex.EncodeToString(hashBytes[:])

	// 4. Cluster Signature
	if cm.clusterKey == nil {
		return nil, fmt.Errorf("mixer cluster key is required to sign imported wasm payloads")
	}

	clusterSig, err := cm.clusterKey.SignBytes(hashBytes[:])
	if err != nil {
		return nil, fmt.Errorf("failed to sign wasm payload: %w", err)
	}

	// 5. Append Cluster Signature
	// Note: We append the cluster signature to the payload that MIGHT still contain the vendor signature
	// if we wanted to preserve it. Let's append to the original payload so the vendor signature remains.
	signedPayload, err := AppendCustomSection(payload, "fluxrig.cluster.signature", clusterSig)
	if err != nil {
		return nil, fmt.Errorf("failed to append cluster signature: %w", err)
	}

	// 6. Save to Disk
	savePath := filepath.Join(cm.catalogDir, hashStr+".wasm")
	if err := os.WriteFile(savePath, signedPayload, 0600); err != nil {
		return nil, fmt.Errorf("failed to write wasm file: %w", err)
	}

	// 7. Distribute to KV
	if cm.distStore != nil {
		if _, err := cm.distStore.Put(ctx, "wasm_catalog", hashStr+".wasm", signedPayload); err != nil {
			cm.log.Warn("failed to push wasm to KV store", "hash", hashStr, "error", err)
		}
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
