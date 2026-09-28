// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// FlushArchiverBuffer builds its COPY statement per wire_id, previously by
// formatting the id and the destination path straight into the query text.
// This exercises it end to end with the bound-parameter form: the row must
// still reach the right file, readable back through DuckDB, and the buffer
// table must end up empty.
func TestStore_FlushArchiverBuffer(t *testing.T) {
	s, err := NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if errMigrate := s.Migrate(ctx); errMigrate != nil {
		t.Fatalf("Migrate failed: %v", errMigrate)
	}

	wireID := uuid.New()
	fluxID := uuid.New()
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO archiver_buffer (ts, flux_id, trace_id, wire_id, subject, payload, meta) VALUES (?, ?, ?, ?, ?, ?, ?)",
		time.Now(), fluxID, "trace-flush-1", wireID, "flux.msg.test", []byte("payload-bytes"), `{"k":"v"}`)
	if err != nil {
		t.Fatalf("insert into archiver_buffer failed: %v", err)
	}

	dataDir := t.TempDir()
	if errFlush := s.FlushArchiverBuffer(ctx, dataDir); errFlush != nil {
		t.Fatalf("FlushArchiverBuffer failed: %v", errFlush)
	}

	var remaining int
	if errCount := s.db.QueryRow("SELECT count(*) FROM archiver_buffer").Scan(&remaining); errCount != nil {
		t.Fatalf("query failed: %v", errCount)
	}
	if remaining != 0 {
		t.Errorf("expected archiver_buffer to be empty after flush, found %d row(s)", remaining)
	}

	pattern := filepath.Join(dataDir, "messages", wireID.String(), "*", "*", "*", "*", "*.parquet")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one parquet file at %s, found %v", pattern, matches)
	}

	var gotTraceID string
	row := s.db.QueryRow("SELECT trace_id FROM read_parquet(?) WHERE flux_id = ?", matches[0], fluxID)
	if err := row.Scan(&gotTraceID); err != nil {
		t.Fatalf("reading the flushed parquet file failed: %v", err)
	}
	if gotTraceID != "trace-flush-1" {
		t.Errorf("expected trace_id %q in the flushed file, got %q", "trace-flush-1", gotTraceID)
	}
}
