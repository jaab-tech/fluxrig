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

	// 3. Registry Operations (Mixer)
	mixerMachineID := uuid.New()
	mixerEID := uuid.New()
	if errReg := s.RegisterMixer(ctx, mixerMachineID, "test-mixer", mixerEID, "localhost:8080", "v1"); errReg != nil {
		t.Fatalf("RegisterMixer failed: %v", errReg)
	}

	id, err := s.GetEntityIDByName(ctx, "test-mixer")
	if err != nil {
		t.Errorf("GetEntityIDByName failed: %v", err)
	}
	if id != mixerEID {
		t.Errorf("Expected ID %s, got %s", mixerEID, id)
	}

	// 4. Registry Interface (Rack)
	machineID := uuid.New()
	rack, err := s.Register(ctx, machineID, "test-rack", "secret", "127.0.0.1", 9000, "v0.4.6", nil, mixerEID)
	if err != nil {
		t.Fatalf("Register rack failed: %v", err)
	}
	if rack.Status != "pending" {
		t.Errorf("Expected status pending, got %s", rack.Status)
	}

	// Approve
	rack, err = s.Approve(ctx, machineID, "approved-rack")
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if rack.Status != "active" || rack.Name != "approved-rack" {
		t.Errorf("Unexpected rack state: %+v", rack)
	}

	// 5. Registry Operations (Snake)
	snakeEID := uuid.New()
	rackEID := uuid.New()
	snakeMachineID := uuid.New()
	if errSnake := s.RegisterSnake(ctx, "test-snake", snakeEID, "v1", rackEID, mixerEID, "1.2.3.4", 9000, "127.0.0.1", 8080, snakeMachineID); errSnake != nil {
		t.Fatalf("RegisterSnake failed: %v", errSnake)
	}

	stats := map[string]any{"uptime": "1h"}
	if errStats := s.UpdateSnakeStats(ctx, snakeEID, stats); errStats != nil {
		t.Errorf("UpdateSnakeStats failed: %v", errStats)
	}

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

	// 10. Cleanup
	if err := s.RemoveSnake(ctx, snakeEID); err != nil {
		t.Errorf("RemoveSnake failed: %v", err)
	}
	if err := s.ClearSnakes(ctx); err != nil {
		t.Errorf("ClearSnakes failed: %v", err)
	}
}
