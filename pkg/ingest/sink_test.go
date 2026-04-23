// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestTelemetrySink_Logs(t *testing.T) {
	mockBus := bus.NewMockBus()
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	defer func() { _ = store.Close() }()

	// Init Schema
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer", 1*time.Minute, nil)
	if err := sink.Start(); err != nil {
		t.Fatalf("Failed to start sink: %v", err)
	}
	defer func() { _ = sink.Stop() }()

	// Create Log Batch
	logMsg := fluxmsg.New()
	logMsg.Metadata["type"] = "telemetry.batch.logs"

	logData := map[string]interface{}{
		"timestamp":  int64(1700000000000000),
		"machine_id": "test-machine",
		"trace_id":   "trace-1",
		"span_id":    "span-1",
		"severity":   "INFO",
		"body":       "test log message",
		"attributes": map[string]interface{}{"component": "test"},
	}
	logMsg.Data = map[string]interface{}{
		"batch": []interface{}{logData},
	}

	if err := mockBus.Publish(context.Background(), "flux.telemetry.logs", logMsg); err != nil {
		t.Fatalf("Failed to publish logs: %v", err)
	}

	// Poll DB
	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		row := store.DB().QueryRow("SELECT body FROM telemetry_logs WHERE trace_id = ?", "trace-1")
		var body string
		if err := row.Scan(&body); err == nil && body == "test log message" {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Error("Log record not found in DuckDB")
	}
}

func TestTelemetrySink_Lifecycle(t *testing.T) {
	// 1. Setup Mock Bus
	mockBus := bus.NewMockBus()

	// 2. Setup Test DB
	tmpFile := "test_sink.db"
	store, err := duckdb.NewStore(slog.Default(), tmpFile)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() {
		_ = store.Close()
		_ = os.Remove(tmpFile)
	}()

	// Init Schema for persistence test
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// 3. Init Sink
	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer", 1*time.Minute, nil)

	// 4. Start
	if err := sink.Start(); err != nil {
		t.Fatalf("Failed to start sink: %v", err)
	}

	// Verify Subscription
	if len(mockBus.Handlers) == 0 {
		t.Error("Expected subscription to be registered")
	}

	// 5. Test Persistence (Trace)
	// We simulate a message on the bus
	traceMsg := fluxmsg.New()
	traceMsg.Metadata["type"] = "telemetry.batch.spans"

	// Construct a raw batch payload matching what NatsExporter sends
	spanData := map[string]interface{}{
		"trace_id":   "12345678901234567890123456789012",
		"span_id":    "1234567890123456",
		"name":       "test-span-ingest",
		"start_time": int64(1700000000000000), // Micros
		"end_time":   int64(1700000001000000),
		"machine_id": "test-machine",
		"attributes": map[string]interface{}{"foo": "bar"},
	}
	traceMsg.Data = map[string]interface{}{
		"batch": []interface{}{spanData},
	}

	// We can't publish easily because Sink uses Subscribe, not a persistent queue we can inspect via Store immediately without a little wait/sync.
	// But MockBus executes handlers synchronously in the same goroutine if they are registered!
	// Wait, MockBus.Publish -> go handler(msg). It is async in generic mock usually.
	// Let's check MockBus implementation.
	// In step 1337: "go handler(msg)" -> It is async.

	// So we need to Publish and then wait/poll DB.

	if err := mockBus.Publish(context.Background(), "flux.telemetry.spans", traceMsg); err != nil {
		t.Fatalf("Failed to publish trace: %v", err)
	}

	// Poll DB for result
	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		// Check Spans table
		// API: InitializeTelemetrySchema must be called!
		// We forgot to init schema in setup?
		// NewStore doesn't init schema automatically?
		// store.go says InitializeSchema and InitializeTelemetrySchema are methods.
		// We need to call them.

		row := store.DB().QueryRow("SELECT count(*) FROM telemetry_spans WHERE trace_id = ?", "12345678901234567890123456789012")
		var count int
		if err := row.Scan(&count); err == nil && count > 0 {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Error("Synthesized span not found in DuckDB after ingestion")
	}

	// 6. Stop
	if err := sink.Stop(); err != nil {
		t.Fatalf("Failed to stop sink: %v", err)
	}
}

func TestTelemetrySink_SchemaInit(t *testing.T) {
	// Helper to separate schema init verification
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// Check table existence
	_, err := store.DB().Exec("SELECT * FROM telemetry_spans LIMIT 0")
	if err != nil {
		t.Errorf("spans table missing: %v", err)
	}
}

func TestTelemetrySink_Metrics(t *testing.T) {
	mockBus := bus.NewMockBus()
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	_ = store.Migrate(ctx)

	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer", 1*time.Minute, nil)
	// Start sink
	_ = sink.Start()
	defer func() { _ = sink.Stop() }()

	// 1. Batch Metrics
	batchMsg := fluxmsg.New()
	batchMsg.Metadata["type"] = "telemetry.batch.metrics"
	batchMsg.Data = map[string]interface{}{
		"batch": []interface{}{
			map[string]interface{}{
				"timestamp":   int64(1700000000000000),
				"entity_id":   100,
				"entity_name": "test-metric",
				"name":        "cpu",
				"type":        "gauge",
				"value":       50.0,
			},
		},
	}
	_ = mockBus.Publish(context.Background(), "flux.telemetry.metrics", batchMsg)

	// 2. Single Metric
	singleMsg := fluxmsg.New()
	singleMsg.Metadata["type"] = "telemetry.metric"
	singleMsg.Data = map[string]interface{}{
		"timestamp":   time.Now().UnixMicro(),
		"entity_id":   101,
		"entity_name": "single-metric",
		"name":        "mem",
		"type":        "gauge",
		"value":       1024,
	}
	_ = mockBus.Publish(context.Background(), "flux.telemetry.metric", singleMsg)

	// Poll
	deadline := time.Now().Add(2 * time.Second)
	foundBatch, foundSingle := false, false
	for time.Now().Before(deadline) {
		if !foundBatch {
			var cnt int
			_ = store.DB().QueryRow("SELECT count(*) FROM telemetry_metrics WHERE name='cpu'").Scan(&cnt)
			if cnt > 0 {
				foundBatch = true
			}
		}
		if !foundSingle {
			var cnt int
			_ = store.DB().QueryRow("SELECT count(*) FROM telemetry_metrics WHERE name='mem'").Scan(&cnt)
			if cnt > 0 {
				foundSingle = true
			}
		}
		if foundBatch && foundSingle {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !foundBatch {
		t.Error("Batch metric not found")
	}
	if !foundSingle {
		t.Error("Single metric not found")
	}
}

func TestTelemetrySink_MiscLogs(t *testing.T) {
	mockBus := bus.NewMockBus()
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	_ = store.Migrate(ctx)

	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer", 1*time.Minute, nil)
	_ = sink.Start()
	defer func() { _ = sink.Stop() }()

	// 1. Single Log JSON
	jsonMsg := fluxmsg.New()
	jsonMsg.Metadata["type"] = "telemetry.log.json"
	// Record is raw json
	jsonMsg.Data = map[string]interface{}{
		"record": json.RawMessage(`{"entity_name":"json-log","body":"hello json","timestamp":1700000000000000}`),
	}
	_ = mockBus.Publish(context.Background(), "flux.telemetry.log.json", jsonMsg)

	// 2. Single Log CBOR (WAL style)
	walMsg := fluxmsg.New()
	walMsg.Metadata["type"] = "telemetry.log"
	// Data IS the log
	walMsg.Data = map[string]interface{}{
		"entity_name": "wal-log",
		"body":        "hello wal",
		"timestamp":   int64(1700000000000000),
	}
	_ = mockBus.Publish(context.Background(), "flux.telemetry.log", walMsg)

	// Poll
	deadline := time.Now().Add(2 * time.Second)
	foundJSON, foundWAL := false, false
	for time.Now().Before(deadline) {
		if !foundJSON {
			var cnt int
			_ = store.DB().QueryRow("SELECT count(*) FROM telemetry_logs WHERE entity_name='json-log'").Scan(&cnt)
			if cnt > 0 {
				foundJSON = true
			}
		}
		if !foundWAL {
			var cnt int
			_ = store.DB().QueryRow("SELECT count(*) FROM telemetry_logs WHERE entity_name='wal-log'").Scan(&cnt)
			if cnt > 0 {
				foundWAL = true
			}
		}
		if foundJSON && foundWAL {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !foundJSON {
		t.Error("JSON log not found")
	}
	if !foundWAL {
		t.Error("WAL log not found")
	}
}
