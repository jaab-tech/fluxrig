// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration defines a single database state transition.
type Migration struct {
	Version int
	Name    string
	Up      func(context.Context, *sql.Tx) error
}

// migrations list - APPEND ONLY.
// We use integer versions matching the slice index + 1 for simplicity,
// or explicit version checks.
// PRAGMA user_version starts at 0.
var migrations = []Migration{
	{
		Version: 1,
		Name:    "init_registry_and_types",
		Up:      migrateV1,
	},
	{
		Version: 2,
		Name:    "init_telemetry_tables",
		Up:      migrateV2,
	},
	{
		Version: 3,
		Name:    "init_archiver_buffer",
		Up:      migrateV3,
	},
}

// Migrate runs pending migrations.
func (s *Store) Migrate(ctx context.Context) error {
	s.log.Info("checking database migrations")

	// 0. Ensure Version Table Exists
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS _schema_version (version INTEGER)"); err != nil {
		return fmt.Errorf("failed to create version table: %w", err)
	}

	// 1. Get Current Version
	var currentVersion int
	row := s.db.QueryRowContext(ctx, "SELECT version FROM _schema_version LIMIT 1")
	if err := row.Scan(&currentVersion); err != nil {
		if err == sql.ErrNoRows {
			currentVersion = 0
			// Initialize
			if _, errExec := s.db.ExecContext(ctx, "INSERT INTO _schema_version VALUES (0)"); errExec != nil {
				return errExec
			}
		} else {
			return fmt.Errorf("failed to query schema version: %w", err)
		}
	}

	if currentVersion >= len(migrations) {
		s.log.Info("database is up to date", "version", currentVersion)
		return nil
	}

	// 2. Run Pending Migrations
	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}

		s.log.Info("applying migration", "version", m.Version, "name", m.Name)

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin tx for migration %d: %w", m.Version, err)
		}

		if err := m.Up(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s) failed: %w", m.Version, m.Name, err)
		}

		// Update Version
		if _, err := tx.ExecContext(ctx, "UPDATE _schema_version SET version = ?", m.Version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to update version to %d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", m.Version, err)
		}
	}

	return nil
}

// --- Migration Steps ---

// V1: Registry & Entity Types
func migrateV1(ctx context.Context, tx *sql.Tx) error {
	query := `
	CREATE TABLE IF NOT EXISTS registry (
		entity_id UBIGINT PRIMARY KEY,
		type_id USMALLINT,
		machine_id USMALLINT,
		mixer_id UBIGINT,
		name TEXT,
		status TEXT DEFAULT 'offline',
		version TEXT,
		started_at TIMESTAMP,
		last_seen TIMESTAMP,
		stats JSON,
		config JSON,
		attributes JSON
	);
	
	CREATE SEQUENCE IF NOT EXISTS seq_machine_id_server START 100;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_name ON registry (name);
	
	CREATE TABLE IF NOT EXISTS entity_types (
		id USMALLINT PRIMARY KEY,
		name TEXT
	);
	`
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return err
	}

	// Seed entity types?
	// We can do it here or let the app do it. Better here for consistency.
	// But getting idgen dependency here creates cyclic dep if idgen depends on store?
	// Store depends on idgen in store.go. So it is fine.
	// However, to keep migrations pure SQL if possible, we might hardcode or pass data.
	// Let's rely on the fact that existing logic handles idempotency or add simple inserts.
	// For now, simple schema.
	return nil
}

// V2: Telemetry Tables
func migrateV2(ctx context.Context, tx *sql.Tx) error {
	// Logs
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS telemetry_logs (
			timestamp TIMESTAMP,
			entity_id UBIGINT,
			entity_type TEXT,
			entity_name TEXT,
			trace_id TEXT,
			span_id TEXT,
			severity TEXT,
			source_file TEXT,
			source_line INTEGER,
			source_func TEXT,
			body TEXT,
			attributes JSON
		);
	`); err != nil {
		return err
	}

	// Spans
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS telemetry_spans (
			start_time TIMESTAMP,
			end_time TIMESTAMP,
			entity_id UBIGINT,
			entity_name TEXT,
			trace_id TEXT,
			span_id TEXT,
			parent_span_id TEXT,
			name TEXT,
			kind TEXT,
			status_code TEXT,
			status_message TEXT,
			attributes JSON
		);
	`); err != nil {
		return err
	}

	// Metrics
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS telemetry_metrics (
			timestamp TIMESTAMP,
			entity_id UBIGINT,
			entity_name TEXT,
			name TEXT,
			description TEXT,
			type TEXT,
			value DOUBLE,
			unit TEXT,
			attributes JSON
		);
	`); err != nil {
		return err
	}

	return nil
}

// V3: Archiver Buffer
func migrateV3(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS archiver_buffer (
			ts BIGINT,
			flux_id UBIGINT,
			trace_id TEXT,
			wire_id UBIGINT,
			subject TEXT,
			payload BLOB,
			meta JSON
		);
	`)
	return err
}
