// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadStoreKeyFile(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("k", MinStoreKeyLen)

	ok := filepath.Join(dir, "ok.key")
	require.NoError(t, os.WriteFile(ok, []byte("\n  "+long+"  \n"), 0o600))
	got, err := LoadStoreKeyFile(ok)
	require.NoError(t, err)
	assert.Equal(t, long, got, "white space around the key is ignored")

	short := filepath.Join(dir, "short.key")
	require.NoError(t, os.WriteFile(short, []byte("too-short"), 0o600))
	_, err = LoadStoreKeyFile(short)
	assert.Error(t, err, "a short key is a password and is refused")

	_, err = LoadStoreKeyFile(filepath.Join(dir, "missing.key"))
	assert.Error(t, err)
}
