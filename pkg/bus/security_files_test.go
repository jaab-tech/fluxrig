// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeMode(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("secret"), mode))
	return path
}

// A key file anyone on the machine can read lets any local user speak to NATS
// as its owner; one restricted to its owner, or one that does not exist, does
// not raise the warning.
func TestWorldReadable(t *testing.T) {
	assert.False(t, WorldReadable(writeMode(t, 0o600)), "owner-only must not warn")
	assert.True(t, WorldReadable(writeMode(t, 0o640)), "group-readable must warn")
	assert.True(t, WorldReadable(writeMode(t, 0o644)), "world-readable must warn")
	assert.False(t, WorldReadable(filepath.Join(t.TempDir(), "missing")),
		"a missing path fails elsewhere, not here")
}

// KeyFiles lists the credential and key files the client reads, and only
// those: the startup warning walks exactly this list.
func TestSecurity_KeyFiles(t *testing.T) {
	s := Security{RootCA: "ca", ClientCert: "cert", ClientKey: "key", Credentials: "creds"}
	assert.Equal(t, []string{"ca", "cert", "key", "creds"}, s.KeyFiles())
	assert.Empty(t, Security{}.KeyFiles(), "nothing set reads no file")
}
