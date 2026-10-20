// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/lockfile"
)

// Before this fix, Migrate's version-check-then-apply sequence had no
// synchronization: several goroutines racing it could all read the same
// pre-migration version and all attempt migrateV1, which seeds fixed-PK rows
// a second insert conflicts on. A Mutex closes that within one process.
func TestStore_MigrateIsSafeUnderConcurrentGoroutines(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "flux.duckdb")
	s, err := NewStore(slog.Default(), dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Migrate(context.Background())
		}()
	}
	wg.Wait()
	close(errs)

	for errMigrate := range errs {
		if errMigrate != nil {
			t.Errorf("concurrent Migrate call failed: %v", errMigrate)
		}
	}

	var count int
	if errQuery := s.db.QueryRow("SELECT count(*) FROM entity_types").Scan(&count); errQuery != nil {
		t.Fatalf("query failed: %v", errQuery)
	}
	if count != 13 {
		t.Errorf("expected exactly 13 seeded entity types, got %d (migration applied more than once)", count)
	}
}

// A Mutex only serializes callers inside one process. Two Mixer processes
// pointed at the same data directory each have their own Store and their own
// Mutex, so only a lock that lives outside the process - a flock on a file
// beside the store - closes the race between them. This simulates the second
// process by holding that same lock externally.
func TestStore_MigrateRefusesWhenAnotherProcessHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flux.duckdb")

	externalLock, err := lockfile.Acquire(filepath.Join(dir, ".migrate.lock"))
	if err != nil {
		t.Fatalf("failed to acquire the simulated external lock: %v", err)
	}

	s, err := NewStore(slog.Default(), dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	if errMigrate := s.Migrate(context.Background()); errMigrate == nil {
		t.Error("Migrate must refuse to run while another process holds the migration lock")
	}

	if errRelease := externalLock.Release(); errRelease != nil {
		t.Fatalf("failed to release the simulated external lock: %v", errRelease)
	}

	if errMigrate := s.Migrate(context.Background()); errMigrate != nil {
		t.Errorf("Migrate must succeed once the lock is free: %v", errMigrate)
	}
}

// An in-memory store cannot be shared across processes, so Migrate must not
// try to flock a file for it.
func TestStore_MigrateSkipsTheFileLockForAnInMemoryStore(t *testing.T) {
	s, err := NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	if errMigrate := s.Migrate(context.Background()); errMigrate != nil {
		t.Errorf("Migrate failed for an in-memory store: %v", errMigrate)
	}
}

// Version 2 deletes the Mixer, Rack and Snake rows, secrets included, now
// that the key-value registry holds them, and keeps the topology rows.
func TestStore_MigrateV2DropsIdentityRowsKeepsTopology(t *testing.T) {
	s, err := NewStore(slog.Default(), "")
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	require.NoError(t, s.Migrate(ctx))

	for i, typeID := range []int{2, 4, 9, 5} {
		_, errIns := s.DB().ExecContext(ctx,
			"INSERT INTO registry (entity_id, type_id, machine_id, name, secret) VALUES (?, ?, ?, ?, 'plaintext')",
			uuid.New(), typeID, uuid.New(), fmt.Sprintf("entity-%d", i))
		require.NoError(t, errIns)
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, migrateV2(ctx, tx))
	require.NoError(t, tx.Commit())

	var identity, topology int
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT count(*) FROM registry WHERE type_id IN (2, 4, 9)").Scan(&identity))
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT count(*) FROM registry WHERE type_id = 5").Scan(&topology))
	assert.Equal(t, 0, identity)
	assert.Equal(t, 1, topology)
}
