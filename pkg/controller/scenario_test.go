// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestScenarioController_Metadata(t *testing.T) {
	// 1. Setup
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)

	ctx := context.Background()

	// 2. Import
	yamlData := []byte(`
meta:
  version: "1.0.0"
gears: []
wires: []
racks: []
`)
	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// 3. List
	list, err := sc.List()
	if err != nil {
		t.Errorf("List failed: %v", err)
	}
	if len(list) != 1 || list[0] != name {
		t.Errorf("List mismatch: %v", list)
	}

	// 4. Before Activation
	if sc.GetActiveName() != "" {
		t.Error("Active name should be empty")
	}
	if sc.CurrentVersion() != "none" {
		t.Error("Current version should be none")
	}

	// 5. Activate
	if err := sc.Activate(ctx, name); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// 6. After Activation
	if sc.GetActiveName() != name {
		t.Errorf("Active name mismatch: %s", sc.GetActiveName())
	}
	if sc.CurrentVersion() != "1.0.0" {
		t.Errorf("Version mismatch: %s", sc.CurrentVersion())
	}
	if sc.GetActiveScenario() == nil {
		t.Error("Active scenario is nil")
	}
}

func TestScenarioController_ComplexActivation(t *testing.T) {
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)
	sc.SetBus(&MockScenarioBus{}) // prevent nil bus error

	ctx := context.Background()

	yamlData := []byte(`
meta:
  version: "2.0.0"
racks:
  - name: "rack-1"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
wires:
  - from: "gear-1.out"
    to: "gear-1.in"
`)
	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-register rack-1 (Simulate enrollment)
	store.SetAutoAdopt(true)
	if _, err := store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID); err != nil {
		t.Fatalf("Failed to pre-register rack: %v", err)
	}

	// Activate triggers registerScenarioEntities
	if err := sc.Activate(ctx, name); err != nil {
		t.Fatal(err)
	}

	// Verify Registry
	if _, err := store.GetRackByName(ctx, "rack-1"); err != nil {
		t.Errorf("Rack used in scenario not registered/active: %v", err)
	}

	if _, err := store.GetEntityIDByName(ctx, "gear-1"); err != nil {
		t.Errorf("Gear not registered: %v", err)
	}
}

// Mock Scenario Bus (fluxmsg compatible)
type MockScenarioBus struct {
	PublishedTopic string
	PublishedMsg   *fluxmsg.FluxMsg
}

func (m *MockScenarioBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	m.PublishedTopic = subject
	m.PublishedMsg = msg
	return nil
}

func TestScenarioController_LifeCycle(t *testing.T) {
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if errMig := store.Migrate(context.Background()); errMig != nil {
		t.Fatal(errMig)
	}

	mockBus := &MockScenarioBus{}
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)

	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)
	sc.SetBus(mockBus)

	ctx := context.Background()

	yamlData := []byte(`
meta:
  version: "1.0.0"
  description: "Test Scenario"
gears:
  - name: "test-gear"
    type: "native"
    deploy: "test-rack"
wires: []
racks:
  - name: "test-rack"
`)

	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	store.SetAutoAdopt(true)
	if _, err := store.Register(ctx, uuid.New(), "test-rack", "sec", "ip", 80, "v1", nil, mixerID); err != nil {
		t.Fatalf("Failed to pre-register rack: %v", err)
	}

	if err := sc.Activate(ctx, name); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	active := sc.GetActiveScenario()
	if active == nil {
		t.Fatal("Active scenario is nil after activation")
	}

	if err := sc.PushActiveToRack(ctx, "test-rack"); err != nil {
		t.Errorf("PushActiveToRack failed: %v", err)
	}
}
