// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestKeysStoreKey_PrintsTheDerivedKeyAndNothingElse(t *testing.T) {
	cluster, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "cluster.key")
	require.NoError(t, cluster.Save(keyPath))
	want, err := cluster.DeriveStoreKey()
	require.NoError(t, err)

	var out bytes.Buffer
	keysStoreKeyCmd.SetOut(&out)
	require.NoError(t, keysStoreKeyCmd.RunE(keysStoreKeyCmd, []string{keyPath}))

	assert.Equal(t, want+"\n", out.String())
}

func TestKeysStoreKey_MissingFileIsAnError(t *testing.T) {
	err := keysStoreKeyCmd.RunE(keysStoreKeyCmd, []string{filepath.Join(t.TempDir(), "missing.key")})
	assert.Error(t, err)
}
