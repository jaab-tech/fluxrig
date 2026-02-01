// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ingest_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestTelemetrySink_Comprehensive(t *testing.T) {
	mockBus := bus.NewMockBus()
	// Use In-Memory DB
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	sink := ingest.NewTelemetrySink(mockBus, store, "comprehensive-mixer", 1*time.Minute, nil)
	if err := sink.Start(); err != nil {
		t.Fatalf("Failed to start sink: %v", err)
	}
	defer func() { _ = sink.Stop() }()

	// 1. Metric Injection
	metricMsg := fluxmsg.New()
	metricMsg.Metadata["type"] = "telemetry.metric"
	// Payload matches NatsWriter.Export for metrics (simple payload, not batch)
	metricData := map[string]any{
		"name":        "cpu_usage",
		"type":        "gauge",
		"value":       85.5,
		"timestamp":   time.Now().UnixMicro(),
		"entity_id":   uint64(100),
		"entity_name": "test-rack",
		"attributes":  map[string]any{"core": "0"},
	}
	metricMsg.Data = metricData // Not 'batch', direct payload

	if err := mockBus.Publish(context.Background(), "flux.telemetry.metrics", metricMsg); err != nil {
		t.Fatalf("Failed to publish metric: %v", err)
	}

	// 2. Poll for Metric
	deadline := time.Now().Add(2 * time.Second)
	foundMetric := false
	for time.Now().Before(deadline) {
		row := store.DB().QueryRow("SELECT value FROM telemetry_metrics WHERE name = 'cpu_usage'")
		var val float64
		if err := row.Scan(&val); err == nil && val == 85.5 {
			foundMetric = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundMetric {
		t.Error("Metric not found in DB")
	}

	// 3. Invalid Message Handling (Should not panic)
	badMsg := fluxmsg.New()
	badMsg.Metadata["type"] = "unknown.type"
	if err := mockBus.Publish(context.Background(), "flux.telemetry.metrics", badMsg); err != nil {
		t.Errorf("Publishing invalid message shouldn't fail publisher: %v", err)
	}
	// Sink just logs warning, no DB change.

	// 4. Broken Payload (Should not panic)
	brokenMsg := fluxmsg.New()
	brokenMsg.Metadata["type"] = "telemetry.batch.spans"
	brokenMsg.Data = map[string]any{"batch": "not-a-list"}
	if err := mockBus.Publish(context.Background(), "flux.telemetry.spans", brokenMsg); err != nil {
		t.Errorf("Publishing broken payload shouldn't fail publisher: %v", err)
	}

	// 5. Verify Span with ParentID (Null checks)
	spanMsg := fluxmsg.New()
	spanMsg.Metadata["type"] = "telemetry.batch.spans"
	spanMsg.Data = map[string]any{
		"batch": []any{
			map[string]any{
				"trace_id": "t1",
				"span_id":  "s1",
				"name":     "root",
				// parent_id missing -> NULL
			},
			map[string]any{
				"trace_id":  "t1",
				"span_id":   "s2",
				"parent_id": "s1", // Has parent
				"name":      "child",
			},
		},
	}
	if err := mockBus.Publish(context.Background(), "flux.telemetry.spans", spanMsg); err != nil {
		t.Fatalf("Failed to publish spans: %v", err)
	}

	// Poll for spans
	foundSpans := 0
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row := store.DB().QueryRow("SELECT count(*) FROM telemetry_spans WHERE trace_id = 't1'")
		var c int
		if err := row.Scan(&c); err == nil {
			foundSpans = c
			if c == 2 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if foundSpans != 2 {
		t.Errorf("Expected 2 spans, got %d", foundSpans)
	}

	// 6. Valid Log Batch
	logBatch := fluxmsg.New()
	logBatch.Metadata["type"] = "telemetry.batch.logs"
	logBatch.Data = map[string]any{
		"batch": []any{
			map[string]any{
				"timestamp":  time.Now().UnixMicro(),
				"machine_id": "test-rack",
				"trace_id":   "t_valid",
				"span_id":    "s_valid",
				"severity":   "ERROR",
				"body":       "critical failure", // Will check this
				"attributes": map[string]any{"code": 500.0},
			},
		},
	}
	if err := mockBus.Publish(context.Background(), "flux.telemetry.logs", logBatch); err != nil {
		t.Fatalf("Failed to publish log batch: %v", err)
	}

	// Poll for Log
	foundLog := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Use ID scan
		var body string
		row := store.DB().QueryRow("SELECT body FROM telemetry_logs WHERE trace_id = 't_valid'")
		if err := row.Scan(&body); err == nil && body == "critical failure" {
			foundLog = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundLog {
		t.Error("Log batch item not found")
	}

	// 7. Valid Metric Batch (telemetry.batch.metrics)
	metricBatchMsg := fluxmsg.New()
	metricBatchMsg.Metadata["type"] = "telemetry.batch.metrics"
	metricBatchMsg.Data = map[string]any{
		"batch": []any{
			map[string]any{
				"name":        "memory_usage",
				"type":        "gauge",
				"value":       1024.0,
				"timestamp":   time.Now().UnixMicro(),
				"entity_id":   uint64(100),
				"entity_name": "test-rack",
				"attributes":  map[string]any{"unit": "MB"},
			},
		},
	}
	if err := mockBus.Publish(context.Background(), "flux.telemetry.metrics", metricBatchMsg); err != nil {
		t.Fatalf("Failed to publish metric batch: %v", err)
	}

	// Poll for Batched Metric
	foundBatchMetric := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row := store.DB().QueryRow("SELECT value FROM telemetry_metrics WHERE name = 'memory_usage'")
		var val float64
		if err := row.Scan(&val); err == nil && val == 1024.0 {
			foundBatchMetric = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundBatchMetric {
		t.Error("Batched metric not found in DB")
	}

	// 8. Single Log JSON (telemetry.log.json)
	singleLogMsg := fluxmsg.New()
	singleLogMsg.Metadata["type"] = "telemetry.log.json"

	// 'record' field must contain JSON RAW message as per sink.go logic
	logRecord := map[string]any{
		"timestamp":   time.Now().UnixMicro(),
		"entity_id":   uint64(100),
		"entity_name": "test-rack",
		"severity":    "WARN",
		"body":        "single log entry",
		"attributes":  map[string]any{"source": "json-handler"},
	}
	logBytes, _ := json.Marshal(logRecord)

	// IMPORTANT: sink.go expects msg.Data["record"] to be json.RawMessage.
	// In MockBus local publish, it's just passed as is.
	// If sink casts to json.RawMessage, it might fail if we pass []byte or string directly?
	// json.RawMessage IS []byte.
	singleLogMsg.Data = map[string]any{
		"record": json.RawMessage(logBytes),
	}

	if err := mockBus.Publish(context.Background(), "flux.telemetry.logs", singleLogMsg); err != nil {
		t.Fatalf("Failed to publish single log: %v", err)
	}

	// Poll for Single Log
	foundSingleLog := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var body string
		row := store.DB().QueryRow("SELECT body FROM telemetry_logs WHERE body = 'single log entry'")
		if err := row.Scan(&body); err == nil && body == "single log entry" {
			foundSingleLog = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundSingleLog {
		t.Error("Single log JSON not found in DB")
	}
}
