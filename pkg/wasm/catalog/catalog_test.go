// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// mockStore implements the Store interface for testing
type mockStore struct {
	data map[string][]byte
}

func (m *mockStore) Put(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[bucket+"/"+key] = value
	return 1, nil
}

func TestCatalogManager_Import(t *testing.T) {
	SkipSandboxValidationForTests = true
	defer func() { SkipSandboxValidationForTests = false }()

	validWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	tmpDir := t.TempDir()
	catalogDir := filepath.Join(tmpDir, "catalog")
	keysDir := filepath.Join(tmpDir, "keys")

	// Generate a Cluster Key for the Mixer
	clusterPub, clusterPriv, _ := ed25519.GenerateKey(rand.Reader)
	clusterKey := &pki.ClusterKey{
		Public:  clusterPub,
		Private: clusterPriv,
	}

	// Generate a Vendor Key
	vendorPub, vendorPriv, _ := ed25519.GenerateKey(rand.Reader)
	vendorKeyFile := filepath.Join(keysDir, "test-vendor.pub")
	_ = os.MkdirAll(keysDir, 0750)
	_ = os.WriteFile(vendorKeyFile, vendorPub, 0644)

	store := &mockStore{}

	// Initialize Catalog
	cm, err := NewCatalogManager(log, catalogDir, keysDir, clusterKey, store)
	if err != nil {
		t.Fatalf("failed to create catalog: %v", err)
	}

	// 1. Test Unsigned Import (Should Fail without allowUnsigned=true)
	_, err = cm.Import(ctx, validWasm, false)
	if err == nil || err.Error() != "missing vendor signature (use allow_unsigned to bypass)" {
		t.Fatalf("expected missing vendor signature error, got %v", err)
	}

	// 2. Test Unsigned Import with Bypass (Should Pass validation and sign)
	metaAny, err := cm.Import(ctx, validWasm, true)
	if err != nil {
		t.Fatalf("failed to import valid unsigned wasm: %v", err)
	}
	meta := metaAny.(WasmMetadata)
	if meta.Status != "Untrusted" {
		t.Errorf("expected status Untrusted, got %v", meta.Status)
	}

	// 3. Test Signed Import
	// Sign the wasm payload using the vendor key
	sig := ed25519.Sign(vendorPriv, HashWasm(validWasm))
	signedWasm, err := AppendCustomSection(validWasm, "fluxrig.signature", sig)
	if err != nil {
		t.Fatalf("failed to append vendor signature: %v", err)
	}

	_, err = cm.Import(ctx, signedWasm, false)
	if err != nil {
		t.Fatalf("failed to import signed wasm: %v", err)
	}
}

func TestCatalogManager_List(t *testing.T) {
	SkipSandboxValidationForTests = true
	defer func() { SkipSandboxValidationForTests = false }()

	validWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmpDir := t.TempDir()

	// Generate a Cluster Key for the Mixer
	clusterPub, clusterPriv, _ := ed25519.GenerateKey(rand.Reader)
	clusterKey := &pki.ClusterKey{
		Public:  clusterPub,
		Private: clusterPriv,
	}

	cm, _ := NewCatalogManager(log, filepath.Join(tmpDir, "cat"), filepath.Join(tmpDir, "keys"), clusterKey, nil)

	_, err := cm.Import(ctx, validWasm, true)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	listAny, err := cm.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	list := listAny.([]WasmMetadata)
	if len(list) != 1 {
		t.Errorf("expected 1 module in list, got %d", len(list))
	}
}
