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
		Name:    "init_fluxrig_schema",
		Up:      migrateV1,
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

// V1: Initial fluxrig Schema (128-bit UUID Native)
func migrateV1(ctx context.Context, tx *sql.Tx) error {
	query := `
	-- Registry & Entity Types
	CREATE TABLE IF NOT EXISTS registry (
		entity_id UUID PRIMARY KEY,
		type_id USMALLINT,
		machine_id UUID,
		mixer_id UUID,
		name TEXT,
		status TEXT DEFAULT 'offline',
		version TEXT,
		ip TEXT,
		port INTEGER,
		secret TEXT,
		first_seen TIMESTAMP,
		last_seen TIMESTAMP,
		update_count INTEGER DEFAULT 0,
		stats TEXT,
		config TEXT,
		attributes TEXT
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_name ON registry (name);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_machine ON registry (machine_id, type_id);
	
	CREATE TABLE IF NOT EXISTS entity_types (
		id USMALLINT PRIMARY KEY,
		name TEXT
	);

	-- Telemetry Tables
	CREATE TABLE IF NOT EXISTS telemetry_logs (
		timestamp TIMESTAMP,
		entity_id UUID,
		entity_type TEXT,
		entity_name TEXT,
		trace_id TEXT,
		span_id TEXT,
		severity TEXT,
		source_file TEXT,
		source_line INTEGER,
		source_func TEXT,
		body TEXT,
		attributes TEXT
	);

	CREATE TABLE IF NOT EXISTS telemetry_spans (
		start_time TIMESTAMP,
		end_time TIMESTAMP,
		entity_id UUID,
		entity_name TEXT,
		trace_id TEXT,
		span_id TEXT,
		parent_span_id TEXT,
		name TEXT,
		kind TEXT,
		status_code TEXT,
		status_message TEXT,
		attributes TEXT
	);

	CREATE TABLE IF NOT EXISTS telemetry_metrics (
		timestamp TIMESTAMP,
		entity_id UUID,
		entity_name TEXT,
		name TEXT,
		description TEXT,
		type TEXT,
		value DOUBLE,
		unit TEXT,
		attributes TEXT
	);

	-- Archiver Buffer
	CREATE TABLE IF NOT EXISTS archiver_buffer (
		ts TIMESTAMP,
		flux_id UUID,
		trace_id TEXT,
		wire_id UUID,
		subject TEXT,
		payload BLOB,
		meta TEXT
	);

	-- Populate Entity Types
	INSERT INTO entity_types (id, name) VALUES 
		(0, 'RESERVED'), (1, 'CLUSTER'), (2, 'MIXER'), (3, 'MSG'), (4, 'RACK'), 
		(5, 'GEAR'), (6, 'PORT_IN'), (7, 'PORT_OUT'), (8, 'WIRE'), (9, 'SNAKE'),
		(10, 'SCENARIO'), (11, 'SESSION'), (12, 'SPEC');
	`
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return err
	}

	return nil
}
