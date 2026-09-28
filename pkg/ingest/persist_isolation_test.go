// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func newIsolationTestStore(t *testing.T) *duckdb.Store {
	t.Helper()
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return store
}

// Before this fix, the first row that failed to insert aborted the whole
// batch: persistLogs returned the raw driver error from row one and never
// attempted the rows after it. Dropping the destination table makes every
// row fail the same way, so the "N of M" aggregate message below can only
// appear if all M rows were actually attempted, not just the first.
func TestPersistLogs_IsolatesPerRecordFailuresInsteadOfAbortingTheBatch(t *testing.T) {
	store := newIsolationTestStore(t)
	if _, err := store.DB().Exec("DROP TABLE telemetry_logs"); err != nil {
		t.Fatalf("failed to drop table: %v", err)
	}

	sink := &TelemetrySink{store: store}
	msg := fluxmsg.New()
	msg.Metadata["type"] = TypeBatchLogs
	msg.Data = map[string]any{
		"batch": []any{
			map[string]any{"body": "first", "trace_id": "t1"},
			map[string]any{"body": "second", "trace_id": "t2"},
			map[string]any{"body": "third", "trace_id": "t3"},
		},
	}

	err := sink.persistLogs(msg)
	if err == nil {
		t.Fatal("expected an error: every row's insert must fail with the table gone")
	}
	if !strings.Contains(err.Error(), "3 of 3") {
		t.Errorf("expected the error to report all 3 rows were attempted, got: %v", err)
	}
}

func TestPersistMetrics_IsolatesPerRecordFailuresInsteadOfAbortingTheBatch(t *testing.T) {
	store := newIsolationTestStore(t)
	if _, err := store.DB().Exec("DROP TABLE telemetry_metrics"); err != nil {
		t.Fatalf("failed to drop table: %v", err)
	}

	sink := &TelemetrySink{store: store}
	msg := fluxmsg.New()
	msg.Metadata["type"] = TypeBatchMetrics
	msg.Data = map[string]any{
		"batch": []any{
			map[string]any{"entity_id": uuid.New(), "name": "m1", "value": 1.0},
			map[string]any{"entity_id": uuid.New(), "name": "m2", "value": 2.0},
		},
	}

	err := sink.persistMetrics(msg)
	if err == nil {
		t.Fatal("expected an error: every row's insert must fail with the table gone")
	}
	if !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("expected the error to report both rows were attempted, got: %v", err)
	}
}

func TestPersistSpans_IsolatesPerRecordFailuresInsteadOfAbortingTheBatch(t *testing.T) {
	store := newIsolationTestStore(t)
	if _, err := store.DB().Exec("DROP TABLE telemetry_spans"); err != nil {
		t.Fatalf("failed to drop table: %v", err)
	}

	sink := &TelemetrySink{store: store}
	msg := fluxmsg.New()
	msg.Metadata["type"] = TypeBatchSpans
	msg.Data = map[string]any{
		"batch": []any{
			map[string]any{"trace_id": "t1", "span_id": "s1", "name": "span1"},
			map[string]any{"trace_id": "t2", "span_id": "s2", "name": "span2"},
		},
	}

	err := sink.persistSpans(msg)
	if err == nil {
		t.Fatal("expected an error: every row's insert must fail with the table gone")
	}
	if !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("expected the error to report both rows were attempted, got: %v", err)
	}
}

// A metric with no usable entity_id cannot be attributed to anything and
// must be dropped, but the drop itself must be visible rather than silent:
// before this fix, nothing distinguished a dropped metric from one nobody
// ever sent.
func TestPersistMetrics_DropsNilEntityMetricsRatherThanSilentlyDiscardingThem(t *testing.T) {
	store := newIsolationTestStore(t)
	sink := &TelemetrySink{store: store}

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prevLogger)

	msg := fluxmsg.New()
	msg.Metadata["type"] = TypeMetric
	msg.Data = map[string]any{"name": "orphan", "value": 1.0} // no entity_id

	if err := sink.persistMetrics(msg); err != nil {
		t.Errorf("a dropped nil-entity metric is not itself a persist error: %v", err)
	}

	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM telemetry_metrics").Scan(&count); err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected the nil-entity metric to be dropped, found %d row(s)", count)
	}
	if !strings.Contains(logBuf.String(), "orphan") {
		t.Errorf("expected the drop to be logged, got log output: %q", logBuf.String())
	}
}
