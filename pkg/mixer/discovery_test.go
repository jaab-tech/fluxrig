// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// We verify the logical flow of startSnakeStats without needing a real Snake server
// by manually hitting the logic if possible, or testing its primitives.
func TestApp_DiscoveryResumption(t *testing.T) {
	// reuse the resumeEntitySequence test pattern but for Rack/Snake discovery
	logger := slog.Default()
	store, _ := duckdb.NewStore(logger, ":memory:")
	_ = store.Migrate(context.Background())
	reg := registry.NewDuckDBRegistry(store)
	reg.SetAutoAdopt(true) // Ensure 'active' status

	machineID := uint16(10)
	idGen, _ := idgen.New(machineID)

	// Seed a Rack - Register returns the Rack object with its real ID
	rack, err := reg.Register(context.Background(), "rack-1", "", "1.1.1.1", 1234, "v1", nil, uint64(machineID))
	if err != nil {
		t.Fatalf("Failed to register rack: %v", err)
	}

	// Use the REAL MachineID assigned by the DB
	realIDGen, _ := idgen.New(rack.MachineID)
	rackEID := realIDGen.NewEntityID(idgen.EntityRack, 0) // Registry uses seq 0 for Rack

	// Seed a Snake
	snakeEID := idGen.NewEntityID(idgen.EntitySnake, 2)
	_ = store.RegisterSnake(context.Background(), "snake-rack-1", snakeEID, "v1", rackEID, 999, "1.1.1.1", 1234, "2.2.2.2", 4222, machineID)

	// Verify we can find them
	foundRack, err := store.GetEntityIDByName(context.Background(), "rack-1")
	if err != nil || foundRack != rackEID {
		t.Errorf("Failed to find rack (EID mismatch): got %d, want %d (err: %v)", foundRack, rackEID, err)
	}
}

func TestApp_initTelemetrySimple(t *testing.T) {
	app := &App{}
	cfg := &config.MixerConfig{
		Telemetry: config.TelemetryConfig{
			BatchInterval: "1s",
			MaxBatchSize:  100,
		},
	}
	idGen, _ := idgen.New(1)

	// We don't connect here, just verify it doesn't panic with empty URL
	shutdown, stop := app.initTelemetry("nats://localhost:4222", 1, "test", idGen, nil, nil, "/tmp", cfg, nil)
	if shutdown != nil {
		// If it actually initialized (unlikely without NATS), clean up
		_ = shutdown(context.Background())
	}
	if stop != nil {
		_ = stop()
	}
}
