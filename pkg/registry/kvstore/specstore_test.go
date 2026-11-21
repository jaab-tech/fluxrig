// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

func specDoc(id, version, body string) []byte {
	return []byte("spec:\n  id: " + id + "\n  name: \"" + id + " title\"\n  version: \"" + version + "\"\n  protocol: iso8583\n" + body)
}

func newSpecStore(t *testing.T, maxSize int) *SpecStore {
	t.Helper()
	b, err := EnsureSpecBucket(context.Background(), startJetStream(t), SpecBucketConfig{Name: "specs_test", MaxSize: 1 << 20})
	require.NoError(t, err)
	return NewSpecStore(b, maxSize, nil)
}

// What a document declares names and versions it, the version is immutable,
// and every way of reading it back returns the same bytes.
func TestSpecStore_ImportAndRead(t *testing.T) {
	ctx := context.Background()
	s := newSpecStore(t, 1<<20)

	v1 := specDoc("visa", "1.0.0", "fields: {}\n")
	hash1, name, tag, err := s.ImportContent(ctx, v1, "", "")
	require.NoError(t, err)
	assert.Equal(t, "visa", name)
	assert.Equal(t, "v1.0.0", tag)

	again, _, _, err := s.ImportContent(ctx, v1, "", "")
	require.NoError(t, err, "the same bytes under the same version change nothing")
	assert.Equal(t, hash1, again)

	_, _, _, err = s.ImportContent(ctx, specDoc("visa", "1.0.0", "fields: {x: 1}\n"), "", "")
	require.ErrorIs(t, err, manager.ErrVersionExists)

	v2 := specDoc("visa", "1.2.0", "fields: {y: 2}\n")
	_, _, _, err = s.ImportContent(ctx, v2, "", "")
	require.NoError(t, err)

	for urn, want := range map[string][]byte{"visa:v1.0.0": v1, "visa:latest": v2, hash1: v1, "sha256:" + hash1: v1} {
		got, errLoad := s.Load(ctx, urn)
		require.NoError(t, errLoad, urn)
		assert.Equal(t, want, got, urn)
	}
	_, err = s.Load(ctx, "visa:v9.9.9")
	require.ErrorIs(t, err, registry.ErrNotFound)

	hist, err := s.History(ctx, manager.KindSpec, "visa")
	require.NoError(t, err)
	require.Len(t, hist, 2)
	assert.Equal(t, "v1.2.0", hist[0].Tag, "newest version first")
	assert.True(t, hist[0].Latest)
	assert.False(t, hist[1].Latest)
	assert.Equal(t, "iso8583", hist[0].Protocol)
	assert.Equal(t, "visa title", hist[0].Title)
	assert.Equal(t, int64(len(v2)), hist[0].Size)
	assert.False(t, hist[0].ImportedAt.IsZero())

	_, err = s.History(ctx, manager.KindSpec, "nothing")
	require.ErrorIs(t, err, registry.ErrNotFound)
}

// A document with no version gets the minor after the highest one.
func TestSpecStore_VersionsAnUndeclaredImport(t *testing.T) {
	ctx := context.Background()
	s := newSpecStore(t, 1<<20)
	_, _, tag, err := s.ImportContent(ctx, []byte("name: plain\nbody: 1\n"), "", "")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", tag)
	_, _, tag, err = s.ImportContent(ctx, []byte("name: plain\nbody: 2\n"), "", "")
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", tag)

	all, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "v0.2.0", all[0].Tag)
}

// A spec larger than the cap is refused before anything is written.
func TestSpecStore_SizeCap(t *testing.T) {
	ctx := context.Background()
	s := newSpecStore(t, 64)
	_, _, _, err := s.ImportContent(ctx, specDoc("big", "1.0.0", string(make([]byte, 100))), "", "")
	require.ErrorIs(t, err, manager.ErrTooLarge)
	all, err := s.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// Imports are control writes: a Mixer without the lease may not make one.
func TestSpecStore_NeedsTheLease(t *testing.T) {
	ctx := context.Background()
	js := startJetStream(t)
	b, err := EnsureSpecBucket(ctx, js, SpecBucketConfig{Name: "specs_lease", MaxSize: 1 << 20})
	require.NoError(t, err)
	leaseBucket := NewMemoryBucket(time.Minute)
	lease := NewLease(leaseBucket, uuid.New(), LeaseConfig{TTL: time.Minute, Renew: time.Second}, nil, nil)
	s := NewSpecStore(b, 1<<20, lease)

	_, _, _, err = s.ImportContent(ctx, specDoc("visa", "1.0.0", ""), "", "")
	require.ErrorIs(t, err, registry.ErrNotLeaseHolder)

	held, err := lease.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, held)
	_, _, _, err = s.ImportContent(ctx, specDoc("visa", "1.0.0", ""), "", "")
	require.NoError(t, err)
}

// The first holder copies its local store once. A version the shared store
// holds with other bytes is reported and skipped, never overwritten, and a
// second migration copies nothing.
func TestSpecStore_MigrateFromLocal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	local, err := manager.NewManager(dir)
	require.NoError(t, err)
	for _, doc := range [][]byte{specDoc("visa", "1.0.0", "a: 1\n"), specDoc("mc", "2.0.0", "b: 1\n")} {
		f := filepath.Join(dir, "in.yaml")
		require.NoError(t, os.WriteFile(f, doc, 0o600))
		_, _, _, err = local.Import(ctx, f, "", "")
		require.NoError(t, err)
	}

	s := newSpecStore(t, 1<<20)
	_, _, _, err = s.ImportContent(ctx, specDoc("mc", "2.0.0", "other: 1\n"), "", "")
	require.NoError(t, err)

	res, done, err := s.MigrateFrom(ctx, local, "mixer-1", slog.Default())
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, 1, res.Copied)
	assert.Equal(t, []string{"mc:v2.0.0"}, res.Conflicts)

	got, err := s.Load(ctx, "visa:v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, specDoc("visa", "1.0.0", "a: 1\n"), got)
	kept, err := s.Load(ctx, "mc:v2.0.0")
	require.NoError(t, err)
	assert.Equal(t, specDoc("mc", "2.0.0", "other: 1\n"), kept, "the shared version is not overwritten")

	migrated, err := s.Migrated(ctx)
	require.NoError(t, err)
	assert.True(t, migrated)
	res, done, err = s.MigrateFrom(ctx, local, "mixer-2", slog.Default())
	require.NoError(t, err)
	assert.True(t, done, "a cluster migrates once")
	assert.Zero(t, res.Copied)
}
