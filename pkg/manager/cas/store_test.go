// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package cas

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDiskStore(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "flux-cas-test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewDiskStore(tmpDir)
	require.NoError(t, err)

	content := []byte("Hello FluxRig Spec Manager")
	expectedHash := "a45c1b9c13dc125364ca052c02865cb52d082f7e9b8142f981aaa2b21e51ed69"

	// Test Put
	hash, err := store.Put(content)
	require.NoError(t, err)
	assert.Equal(t, expectedHash, hash)

	// Verify physical file location (Sharding)
	shardPath := filepath.Join(tmpDir, "blobs", "a4", expectedHash)
	assert.FileExists(t, shardPath)

	// Test Get
	readContent, err := store.Get(hash)
	require.NoError(t, err)
	assert.Equal(t, content, readContent)

	// Test Has
	assert.True(t, store.Has(hash))
	assert.False(t, store.Has("deadbeef"))
}

func TestDiskStore_HexValidation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flux-cas-hex")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewDiskStore(tmpDir)
	require.NoError(t, err)

	// 64 chars but contains non-hex characters (uppercase, special chars)
	badInputs := []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // uppercase
		"../../../../etc/passwd/padding_to_reach_64_characters_xxxxxxxxxx", // traversal
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", // non-hex
		"short", // too short
		"",      // empty
	}

	for _, bad := range badInputs {
		_, err := store.Get(bad)
		assert.Error(t, err, "Get should reject: %q", bad)
		assert.False(t, store.Has(bad), "Has should reject: %q", bad)
	}
}
