// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Mock Telemetry Registry
type MockTelemetryRegistry struct {
	*MockRegistry // Embed existing stub (safe if pointer methods match)
}

func (m *MockTelemetryRegistry) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) {
	if query.EntityName == "error" {
		return nil, errors.New("query failed")
	}
	return []registry.LogEntry{
		{Body: "test-log", Severity: "INFO", Timestamp: time.Now()},
	}, nil
}

func (m *MockTelemetryRegistry) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	if query.EntityName == "error" {
		return nil, errors.New("query failed")
	}
	return []registry.MetricEntry{
		{Name: "cpu", Value: 50.0},
	}, nil
}

func TestHandleTelemetry(t *testing.T) {
	// Setup
	mockReg := &MockTelemetryRegistry{&MockRegistry{}}
	s := NewServer(mockReg, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil) // Reg interface satisfied? Yes *MockTelemetryRegistry has methods

	// 1. Logs Success
	req1 := httptest.NewRequest("GET", "/api/v1/telemetry/logs?since=1h&limit=50&min_level=WARN", nil)
	req1.SetPathValue("type", "logs")
	w1 := httptest.NewRecorder()
	s.handleTelemetry(w1, req1)
	if w1.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 logs, got %d", w1.Result().StatusCode)
	}
	var logs []registry.LogEntry
	if err := json.NewDecoder(w1.Body).Decode(&logs); err != nil {
		t.Errorf("Failed to decode logs: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("Expected 1 log, got %d", len(logs))
	}

	// 2. Logs Error
	req2 := httptest.NewRequest("GET", "/api/v1/telemetry/logs?entity=error", nil)
	req2.SetPathValue("type", "logs")
	w2 := httptest.NewRecorder()
	s.handleTelemetry(w2, req2)
	if w2.Result().StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500 logs error, got %d", w2.Result().StatusCode)
	}

	// 3. Metrics Success
	req3 := httptest.NewRequest("GET", "/api/v1/telemetry/metrics?name=cpu&until=2023-01-01T00:00:00Z", nil)
	req3.SetPathValue("type", "metrics")
	w3 := httptest.NewRecorder()
	s.handleTelemetry(w3, req3)
	if w3.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 metrics, got %d", w3.Result().StatusCode)
	}
	var metrics []registry.MetricEntry
	if err := json.NewDecoder(w3.Body).Decode(&metrics); err != nil {
		t.Errorf("Failed to decode metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Errorf("Expected 1 metric, got %d", len(metrics))
	}

	// 4. Metrics Error
	req4 := httptest.NewRequest("GET", "/api/v1/telemetry/metrics?entity=error", nil)
	req4.SetPathValue("type", "metrics")
	w4 := httptest.NewRecorder()
	s.handleTelemetry(w4, req4)
	if w4.Result().StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500 metrics error, got %d", w4.Result().StatusCode)
	}

	// 5. Not Found (Unknown path)
	req5 := httptest.NewRequest("GET", "/api/v1/telemetry/unknown", nil)
	req5.SetPathValue("type", "unknown")
	w5 := httptest.NewRecorder()
	s.handleTelemetry(w5, req5)
	if w5.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", w5.Result().StatusCode)
	}

	// 6. Time Parsing Coverage
	req6 := httptest.NewRequest("GET", "/api/v1/telemetry/logs?since=invalid", nil)
	req6.SetPathValue("type", "logs")
	w6 := httptest.NewRecorder()
	s.handleTelemetry(w6, req6)
	if w6.Result().StatusCode != http.StatusOK {
		t.Error("Invalid time should be ignored/defaulted, not error?")
	}
	// parseTime returns zero time on failure, loop continues.
}
