package ingest_test

import (
	"context"
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
	store, _ := duckdb.NewStore(":memory:")
	defer store.Close()

	// Init Schema
	if err := store.InitializeTelemetrySchema(context.Background()); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer")
	if err := sink.Start(); err != nil {
		t.Fatalf("Failed to start sink: %v", err)
	}
	defer sink.Stop()

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

	if err := mockBus.Publish("flux.telemetry.logs", logMsg); err != nil {
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
	store, err := duckdb.NewStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() {
		store.Close()
		os.Remove(tmpFile)
	}()

	// Init Schema for persistence test
	if err := store.InitializeTelemetrySchema(context.Background()); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// 3. Init Sink
	sink := ingest.NewTelemetrySink(mockBus, store, "test-mixer")

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

	if err := mockBus.Publish("flux.telemetry.spans", traceMsg); err != nil {
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
	store, _ := duckdb.NewStore(":memory:")
	defer store.Close()

	ctx := context.Background()
	if err := store.InitializeTelemetrySchema(ctx); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// Check table existence
	_, err := store.DB().Exec("SELECT * FROM telemetry_spans LIMIT 0")
	if err != nil {
		t.Errorf("spans table missing: %v", err)
	}
}
