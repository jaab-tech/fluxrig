// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// Identical bytes under a version that already names them change nothing,
// and identical bytes under another version are a new version of the same
// artefact: never a VersionExists, which is reserved for other bytes.
func TestSpecStore_IdenticalBytesAreRecognised(t *testing.T) {
	ctx := context.Background()
	s := newSpecStore(t, 1<<20)

	v1 := specDoc("visa", "1.0.0", "fields: {}\n")
	hash, _, _, err := s.ImportContent(ctx, v1, "", "")
	require.NoError(t, err)

	again, _, _, err := s.ImportContent(ctx, v1, "visa", "v1.0.0")
	require.NoError(t, err, "the same bytes under the same version are already present")
	assert.Equal(t, hash, again)

	other, name, tag, err := s.ImportContent(ctx, v1, "visa", "v1.1.0")
	require.NoError(t, err, "the same bytes under another version are a new version, not a conflict")
	assert.Equal(t, hash, other)
	assert.Equal(t, "visa", name)
	assert.Equal(t, "v1.1.0", tag)

	got, err := s.Load(ctx, "visa:v1.1.0")
	require.NoError(t, err)
	assert.Equal(t, v1, got)
}

// Migrating identical bytes, however many versions carry them, copies every
// version and reports no conflict: a conflict is other bytes under the tag.
func TestSpecStore_MigrateCountsIdenticalBytes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	local, err := manager.NewManager(dir)
	require.NoError(t, err)
	doc := specDoc("visa", "1.0.0", "a: 1\n")
	for _, tag := range []string{"v1.0.0", "v1.1.0"} {
		f := filepath.Join(dir, "in.yaml")
		require.NoError(t, os.WriteFile(f, doc, 0o600))
		_, _, _, err = local.Import(ctx, f, "", tag)
		require.NoError(t, err)
	}

	s := newSpecStore(t, 1<<20)
	// The shared store already holds one tag with the same bytes: that is
	// already-present, not a conflict.
	_, _, _, err = s.ImportContent(ctx, doc, "visa", "v1.0.0")
	require.NoError(t, err)

	res, done, err := s.MigrateFrom(ctx, local, "mixer-1", slog.Default())
	require.NoError(t, err)
	assert.False(t, done)
	assert.Empty(t, res.Conflicts)
	assert.Equal(t, 2, res.Copied, "both local versions are now in the shared store")
}
