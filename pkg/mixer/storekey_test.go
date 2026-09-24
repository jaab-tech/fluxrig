// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func clusterKey(t *testing.T) *pki.ClusterKey {
	t.Helper()
	k, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	return k
}

func writeKey(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestResolveStoreKeys_DerivedFromTheClusterKeyByDefault(t *testing.T) {
	cluster := clusterKey(t)
	want, err := cluster.DeriveStoreKey()
	require.NoError(t, err)

	key, oldKey, source, err := resolveStoreKeys(&config.SnakeConfig{StoreEncryption: true}, cluster)

	require.NoError(t, err)
	assert.Equal(t, want, key)
	assert.Empty(t, oldKey)
	assert.Equal(t, storeKeyFromCluster, source)
}

func TestResolveStoreKeys_AnOperatorKeyFileWins(t *testing.T) {
	operator := strings.Repeat("o", 40)
	cfg := &config.SnakeConfig{StoreEncryption: true, StoreKeyFile: writeKey(t, "store.key", operator+"\n")}

	key, _, source, err := resolveStoreKeys(cfg, clusterKey(t))

	require.NoError(t, err)
	assert.Equal(t, operator, key)
	assert.Equal(t, storeKeyFromFile, source)
}

func TestResolveStoreKeys_RotationCarriesThePreviousKey(t *testing.T) {
	previous := strings.Repeat("p", 40)
	cfg := &config.SnakeConfig{StoreEncryption: true, StoreOldKeyFile: writeKey(t, "old.key", previous)}

	_, oldKey, _, err := resolveStoreKeys(cfg, clusterKey(t))

	require.NoError(t, err)
	assert.Equal(t, previous, oldKey)
}

func TestResolveStoreKeys_EncryptionOffGivesNoKey(t *testing.T) {
	key, oldKey, source, err := resolveStoreKeys(&config.SnakeConfig{StoreEncryption: false, StoreKeyFile: "ignored"}, clusterKey(t))

	require.NoError(t, err)
	assert.Empty(t, key)
	assert.Empty(t, oldKey)
	assert.Equal(t, storeKeyOff, source)
}

// A key file that cannot be used stops the Mixer: falling back to another key would
// make the store unreadable at the next start.
func TestResolveStoreKeys_UnusableFilesAreErrors(t *testing.T) {
	cluster := clusterKey(t)

	_, _, _, err := resolveStoreKeys(&config.SnakeConfig{StoreEncryption: true, StoreKeyFile: filepath.Join(t.TempDir(), "missing")}, cluster)
	assert.Error(t, err)

	_, _, _, err = resolveStoreKeys(&config.SnakeConfig{StoreEncryption: true, StoreKeyFile: writeKey(t, "short.key", "short")}, cluster)
	assert.Error(t, err)

	_, _, _, err = resolveStoreKeys(&config.SnakeConfig{StoreEncryption: true, StoreOldKeyFile: filepath.Join(t.TempDir(), "missing")}, cluster)
	assert.Error(t, err)
}

func TestParseStreamMaxAge(t *testing.T) {
	d, err := parseStreamMaxAge("36h")
	require.NoError(t, err)
	assert.Equal(t, 36*time.Hour, d)

	d, err = parseStreamMaxAge("")
	require.NoError(t, err)
	assert.Zero(t, d, "empty means no limit")

	_, err = parseStreamMaxAge("soon")
	assert.Error(t, err)
	_, err = parseStreamMaxAge("-1h")
	assert.Error(t, err)
}
