// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

func TestStore_Comprehensive(t *testing.T) {
	// Setup Temp Dir for Telemetry Flush
	tmpDir, err := os.MkdirTemp("", "fluxrig-store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// 1. NewStore (File-based to test Flush)
	dbPath := filepath.Join(tmpDir, "test.duckdb")
	s, err := NewStore(slog.Default(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()

	// 2. Schemas
	if errMig := s.Migrate(ctx); errMig != nil {
		t.Fatalf("Migrate failed: %v", errMig)
	}
	// Rack, Mixer and Snake records live in the key-value registry; this
	// store keeps telemetry and the topology rows below, which reference
	// those IDs without owning them.
	mixerEID := uuid.New()
	machineID := uuid.New()

	// 6. Telemetry Logs Insert & Query
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO telemetry_logs (timestamp, entity_id, entity_name, severity, body, attributes)
		VALUES (?, ?, ?, ?, ?, ?)
	`, time.Now(), mixerEID, "test-mixer", "INFO", "test log", `{"foo":"bar"}`)
	if err != nil {
		t.Fatalf("Manual log insert failed: %v", err)
	}

	logs, err := s.QueryLogs(ctx, registry.LogQuery{Limit: 10})
	if err != nil {
		t.Fatalf("QueryLogs failed: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("Expected 1 log, got %d", len(logs))
	}

	// 7. Metrics Insert & Query
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO telemetry_metrics (timestamp, entity_id, entity_name, name, type, value, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, time.Now(), mixerEID, "test-mixer", "cpu_usage", "gauge", 50.5, `{"core":"1"}`)
	if err != nil {
		t.Fatalf("Manual metric insert failed: %v", err)
	}

	metrics, err := s.QueryMetrics(ctx, registry.MetricQuery{Limit: 10})
	if err != nil {
		t.Fatalf("QueryMetrics failed: %v", err)
	}
	if len(metrics) != 1 {
		t.Errorf("Expected 1 metric, got %d", len(metrics))
	}

	// 8. Flush Telemetry
	if errFlush := s.FlushTelemetry(ctx, tmpDir); errFlush != nil {
		t.Fatalf("FlushTelemetry failed: %v", errFlush)
	}

	// 9. Scenario & Entities Registration
	scenarioEID := uuid.New()
	if err := s.RegisterScenario(ctx, scenarioEID, "test-scenario", "1.0.0", 1, 1, mixerEID); err != nil {
		t.Fatalf("RegisterScenario failed: %v", err)
	}

	gearEID := uuid.New()
	portInEID := uuid.New()
	portOutEID := uuid.New()
	ports := map[string]uuid.UUID{"in": portInEID, "out": portOutEID}
	if err := s.RegisterGear(ctx, gearEID, "test-gear", "native", "active", "8080", "", scenarioEID, machineID, ports, mixerEID); err != nil {
		t.Fatalf("RegisterGear failed: %v", err)
	}

	if err := s.RegisterPort(ctx, portInEID, "test-gear.in", 1, gearEID, scenarioEID, machineID, mixerEID); err != nil {
		t.Fatalf("RegisterPort(in) failed: %v", err)
	}

	wireEID := uuid.New()
	if err := s.RegisterWire(ctx, wireEID, "test-gear.in", "test-gear.out", portInEID, portOutEID, scenarioEID, machineID, mixerEID); err != nil {
		t.Fatalf("RegisterWire failed: %v", err)
	}

}
