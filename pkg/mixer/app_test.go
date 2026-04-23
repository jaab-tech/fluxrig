// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestNewApp(t *testing.T) {
	cfg := &config.MixerConfig{}
	app := NewApp(cfg, "test.yaml")
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.scenarioRef != "test.yaml" {
		t.Errorf("Expected scenarioRef test.yaml, got %s", app.scenarioRef)
	}
}

func TestResumeEntitySequence(t *testing.T) {
	// 1. Setup in-memory DuckDB
	logger := slog.Default()
	store, err := duckdb.NewStore(logger, ":memory:")
	if err != nil {
		t.Fatalf("Failed to open in-memory store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.Migrate(context.Background())
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// 2. Prep data (Register a Mixer to have an entry in registry)
	// We need an entry with a specific machine ID and high EID
	machineID := uint16(5)
	idGen, _ := idgen.New(machineID)
	eid := idGen.NewEntityID(idgen.EntityMixer, 100) // Seq 100

	err = store.RegisterMixer(context.Background(), machineID, "test-mixer", eid, "localhost:8080", "v0.0.1")
	if err != nil {
		t.Fatalf("Failed to seed data: %v", err)
	}

	// 3. Test Resumption
	app := &App{}
	newIDGen, _ := idgen.New(machineID) // Starts at 0

	app.resumeEntitySequence(store, machineID, newIDGen)

	// Next ID should be Seq 101 (resumed 100 + 1)
	next := newIDGen.NextEntityID(idgen.EntityMixer)
	// Seq is the lower 40 bits.
	seq := next & 1099511627775
	if seq != 101 {
		t.Errorf("Expected resumed sequence 101, got %d", seq)
	}
}

func TestResumeEntitySequenceEmpty(t *testing.T) {
	logger := slog.Default()
	store, _ := duckdb.NewStore(logger, ":memory:")
	_ = store.Migrate(context.Background())

	app := &App{}
	machineID := uint16(99)
	idGen, _ := idgen.New(machineID)

	// Should not panic or error if no mixer entry exists
	app.resumeEntitySequence(store, machineID, idGen)

	next := idGen.NextEntityID(idgen.EntityMixer)
	seq := next & 1099511627775
	if seq != 1 {
		t.Errorf("Expected fresh sequence 1, got %d", seq)
	}
}
