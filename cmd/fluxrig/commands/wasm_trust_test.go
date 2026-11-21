// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wasm keys trust takes a public key, or the public half of a signing key,
// and never sends a private seed as if it were a public key.
func TestTrustedPublicKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o600))
		return p
	}

	got, err := trustedPublicKey(write("vendor.pub", pub))
	require.NoError(t, err)
	assert.Equal(t, pub, got)

	_, err = trustedPublicKey(write("vendor.seed", priv.Seed()))
	require.Error(t, err, "a 32-byte file not named .pub can be a private seed")

	got, err = trustedPublicKey(write("vendor.key", priv))
	require.NoError(t, err)
	assert.Equal(t, pub, got, "a signing key file gives its public half")
}
