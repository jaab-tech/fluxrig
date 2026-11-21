// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

// memStore is the store on the bus, in memory. failPut makes every Put fail.
type memStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	failPut bool
}

func (m *memStore) Put(_ context.Context, bucket, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return 0, errors.New("no response from stream")
	}
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[bucket+"/"+key] = append([]byte(nil), value...)
	return 1, nil
}

func (m *memStore) Get(_ context.Context, bucket, key string) ([]byte, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[bucket+"/"+key]
	if !ok {
		return nil, 0, nil
	}
	return v, 1, nil
}

func (m *memStore) Keys(_ context.Context, bucket string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if rest, ok := strings.CutPrefix(k, bucket+"/"); ok {
			keys = append(keys, rest)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

var validWasm = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

type fixture struct {
	t          *testing.T
	store      *memStore
	clusterKey *pki.ClusterKey
	vendorPub  ed25519.PublicKey
	vendorPriv ed25519.PrivateKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	SkipSandboxValidationForTests = true
	t.Cleanup(func() { SkipSandboxValidationForTests = false })
	clusterPub, clusterPriv, _ := ed25519.GenerateKey(rand.Reader)
	vendorPub, vendorPriv, _ := ed25519.GenerateKey(rand.Reader)
	return &fixture{t: t, store: &memStore{}, clusterKey: &pki.ClusterKey{Public: clusterPub, Private: clusterPriv},
		vendorPub: vendorPub, vendorPriv: vendorPriv}
}

// mixer is one Mixer: its own directories, the shared store. withKeyFile puts
// the vendor's key in its keys directory.
func (f *fixture) mixer(withKeyFile bool, store Store) (*CatalogManager, string) {
	f.t.Helper()
	dir := f.t.TempDir()
	keysDir := filepath.Join(dir, "keys")
	if withKeyFile {
		require.NoError(f.t, os.MkdirAll(keysDir, 0750))
		require.NoError(f.t, os.WriteFile(filepath.Join(keysDir, "test-vendor.pub"), f.vendorPub, 0600))
	}
	cm, err := NewCatalogManager(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "catalog"), keysDir, f.clusterKey, store)
	require.NoError(f.t, err)
	require.NoError(f.t, cm.SeedKeyring(context.Background()))
	return cm, filepath.Join(dir, "catalog")
}

func (f *fixture) signed() []byte {
	f.t.Helper()
	out, err := AppendCustomSection(validWasm, "fluxrig.signature", ed25519.Sign(f.vendorPriv, HashWasm(validWasm)))
	require.NoError(f.t, err)
	return out
}

func TestImport_SignaturesAndTheBypass(t *testing.T) {
	f := newFixture(t)
	cm, _ := f.mixer(true, f.store)
	ctx := context.Background()

	_, err := cm.Import(ctx, validWasm, false)
	require.EqualError(t, err, "missing vendor signature (use allow_unsigned to bypass)")

	meta, err := cm.Import(ctx, validWasm, true)
	require.NoError(t, err)
	assert.Equal(t, "Untrusted", meta.Status)

	meta, err = cm.Import(ctx, f.signed(), false)
	require.NoError(t, err)
	assert.Equal(t, "Trusted: test-vendor", meta.Status)
}

// A module imported through one Mixer is listed by another one, and a vendor
// key found on one Mixer is trusted by the other: both read the bus.
func TestList_IsTheSameOnEveryMixer(t *testing.T) {
	f := newFixture(t)
	a, _ := f.mixer(true, f.store)
	b, _ := f.mixer(false, f.store)
	ctx := context.Background()

	meta, err := b.Import(ctx, f.signed(), false)
	require.NoError(t, err, "b trusts the vendor whose key only a holds")
	list, err := a.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, meta.Hash, list[0].Hash)
	assert.Equal(t, "Trusted: test-vendor", list[0].Status)
}

// An import the store does not take is not an import: it reports
// ErrUnavailable, and no local copy stays behind.
func TestImport_StoreRefusalImportsNothing(t *testing.T) {
	f := newFixture(t)
	cm, dir := f.mixer(true, f.store)
	f.store.failPut = true

	_, err := cm.Import(context.Background(), validWasm, true)
	require.ErrorIs(t, err, ErrUnavailable)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "no local copy of a module the store refused")
}

// A revoked vendor is refused on every Mixer, and a Mixer that still has its
// key file does not trust it again when it starts.
func TestRevokeKey_StaysRevoked(t *testing.T) {
	f := newFixture(t)
	a, _ := f.mixer(true, f.store)
	ctx := context.Background()
	require.NoError(t, a.RevokeKey(ctx, "test-vendor"))

	_, err := a.Import(ctx, f.signed(), false)
	require.Error(t, err)
	restarted, _ := f.mixer(true, f.store)
	_, err = restarted.Import(ctx, f.signed(), false)
	require.Error(t, err, "the key file does not undo the revocation")

	keys, err := a.ListKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []TrustedKey{{Vendor: "test-vendor", Revoked: true}}, keys)

	require.NoError(t, a.TrustKey(ctx, "test-vendor", f.vendorPub))
	_, err = restarted.Import(ctx, f.signed(), false)
	require.NoError(t, err, "trusted again through the keyring")
}

func TestTrustKey_RefusesWhatItCannotUse(t *testing.T) {
	f := newFixture(t)
	cm, _ := f.mixer(false, f.store)
	ctx := context.Background()
	assert.ErrorIs(t, cm.TrustKey(ctx, "a vendor", f.vendorPub), ErrInvalidKey)
	assert.ErrorIs(t, cm.TrustKey(ctx, "vendor", []byte("short")), ErrInvalidKey)
}

// Without a store the directories are the catalog and the keyring.
func TestList_WithoutAStoreReadsTheDirectory(t *testing.T) {
	f := newFixture(t)
	cm, _ := f.mixer(true, nil)
	ctx := context.Background()
	_, err := cm.Import(ctx, f.signed(), false)
	require.NoError(t, err)
	list, err := cm.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "Trusted: test-vendor", list[0].Status)
}
