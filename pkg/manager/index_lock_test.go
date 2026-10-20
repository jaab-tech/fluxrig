// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A second process (a CLI beside a Mixer) builds its own DefaultManager over
// the same rootDir. Both do read-modify-write on index.json. Without a
// cross-process lock and a re-read under it, the second writer overwrites the
// first writer's entry with its stale in-memory copy, and the first artifact
// becomes unreachable even though its bytes are still in the CAS.
//
// These two managers stand in for the two processes: each holds its own
// in-memory index, exactly as two processes would.
func TestManager_ConcurrentImportsDoNotLoseEntries(t *testing.T) {
	rootDir := t.TempDir()
	ctx := context.Background()

	// Both managers load the index before either writes: the exact condition
	// that produces a stale copy.
	first, err := NewManager(rootDir)
	require.NoError(t, err)
	second, err := NewManager(rootDir)
	require.NoError(t, err)

	_, _, _, err = first.Import(ctx, writeSpec(t, rootDir, "alpha"), "alpha", "v1.0.0")
	require.NoError(t, err)

	_, _, _, err = second.Import(ctx, writeSpec(t, rootDir, "beta"), "beta", "v1.0.0")
	require.NoError(t, err)

	// A fresh manager reads what is actually on disk.
	reloaded, err := NewManager(rootDir)
	require.NoError(t, err)

	specs := reloaded.index.Specs
	assert.Contains(t, specs, "alpha", "the first import's entry must survive the second import")
	assert.Contains(t, specs, "beta", "the second import's entry must be present")
	assert.Equal(t, "v1.0.0", tagOf(specs, "alpha"))
	assert.Equal(t, "v1.0.0", tagOf(specs, "beta"))

	// The lost update is not only an index problem: the artifact must be
	// reachable by name, which is what Load uses.
	_, err = reloaded.Load(ctx, "alpha:v1.0.0")
	assert.NoError(t, err, "the first artifact must stay reachable after the second import")
}

// Many distinct imports through two managers in parallel. Every name must be
// in the index afterwards; none may be lost to a stale write.
func TestManager_ParallelImportsAcrossManagers(t *testing.T) {
	rootDir := t.TempDir()
	ctx := context.Background()

	const perManager = 8
	managers := make([]*DefaultManager, 2)
	for i := range managers {
		m, err := NewManager(rootDir)
		require.NoError(t, err)
		managers[i] = m
	}

	var wg sync.WaitGroup
	for mi, mgr := range managers {
		wg.Add(1)
		go func(mi int, mgr *DefaultManager) {
			defer wg.Done()
			for j := 0; j < perManager; j++ {
				name := fmt.Sprintf("spec-%d-%d", mi, j)
				path := writeSpec(t, rootDir, name)
				if _, _, _, err := mgr.Import(ctx, path, name, "v1.0.0"); err != nil {
					t.Errorf("import %s: %v", name, err)
				}
			}
		}(mi, mgr)
	}
	wg.Wait()

	reloaded, err := NewManager(rootDir)
	require.NoError(t, err)

	for mi := range managers {
		for j := 0; j < perManager; j++ {
			name := fmt.Sprintf("spec-%d-%d", mi, j)
			assert.Contains(t, reloaded.index.Specs, name, "parallel import %s was lost", name)
		}
	}
}

func writeSpec(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".yaml")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("name: %s\nversion: v1.0.0\ntype: iso8583", name)), 0600))
	return path
}

func tagOf(specs map[string]map[string]string, name string) string {
	if specs[name] == nil {
		return ""
	}
	if _, ok := specs[name]["v1.0.0"]; ok {
		return "v1.0.0"
	}
	return ""
}

// staleManagers returns two managers over one rootDir, both loaded before
// either writes, as a CLI and a Mixer would be.
func staleManagers(t *testing.T) (first, second *DefaultManager, rootDir string) {
	t.Helper()
	rootDir = t.TempDir()
	var err error
	first, err = NewManager(rootDir)
	require.NoError(t, err)
	second, err = NewManager(rootDir)
	require.NoError(t, err)
	return first, second, rootDir
}

// A version is immutable: a stale manager may not rebind it to other bytes.
func TestManager_StaleImportCannotRebindAVersion(t *testing.T) {
	ctx := context.Background()
	first, second, rootDir := staleManagers(t)

	hashA, _, _, err := first.ImportContent(ctx, []byte("name: foo\nversion: v1.0.0\ntype: iso8583\n# A"), "", "")
	require.NoError(t, err)
	_, _, _, err = second.ImportContent(ctx, []byte("name: foo\nversion: v1.0.0\ntype: iso8583\n# B"), "", "")
	require.ErrorContains(t, err, "immutable")

	reloaded, err := NewManager(rootDir)
	require.NoError(t, err)
	assert.Equal(t, hashA, reloaded.index.Specs["foo"]["v1.0.0"], "foo:v1.0.0 still names the first bytes")
}

// An import with no version gets the next free one, counted from the index
// on disk: two stale managers do not both take the same version.
func TestManager_StaleImportsGetDistinctVersions(t *testing.T) {
	ctx := context.Background()
	first, second, rootDir := staleManagers(t)

	_, _, tagA, err := first.ImportContent(ctx, []byte("name: bar\ntype: iso8583\n# A"), "", "")
	require.NoError(t, err)
	_, _, tagB, err := second.ImportContent(ctx, []byte("name: bar\ntype: iso8583\n# B"), "", "")
	require.NoError(t, err)
	assert.NotEqual(t, tagA, tagB, "each import gets its own version")

	reloaded, err := NewManager(rootDir)
	require.NoError(t, err)
	_, err = reloaded.Load(ctx, "bar:"+tagA)
	assert.NoError(t, err, "the first artifact is reachable by name")
	_, err = reloaded.Load(ctx, "bar:"+tagB)
	assert.NoError(t, err, "the second artifact is reachable by name")
}

// "latest" is the highest version on disk, not the highest a stale manager
// had seen.
func TestManager_StaleImportDoesNotMoveLatestBack(t *testing.T) {
	ctx := context.Background()
	first, second, rootDir := staleManagers(t)

	hash2, _, _, err := first.ImportContent(ctx, []byte("name: baz\nversion: v2.0.0\ntype: iso8583"), "", "")
	require.NoError(t, err)
	_, _, _, err = second.ImportContent(ctx, []byte("name: baz\nversion: v1.0.0\ntype: iso8583"), "", "")
	require.NoError(t, err)

	reloaded, err := NewManager(rootDir)
	require.NoError(t, err)
	assert.Equal(t, hash2, reloaded.index.Specs["baz"]["latest"], "latest stays on v2.0.0")
}
