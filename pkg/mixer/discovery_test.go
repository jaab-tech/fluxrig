// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestApp_DiscoveryResumption(t *testing.T) {
	logger := slog.Default()
	store, _ := duckdb.NewStore(logger, ":memory:")
	_ = store.Migrate(context.Background())
	store.SetAutoAdopt(true)

	machineID := uuid.New()
	idGen, _ := idgen.New(machineID)

	// Seed a Rack
	mixerID := uuid.New()
	rack, err := store.Register(context.Background(), uuid.New(), "rack-1", "", "1.1.1.1", 1234, "v1", nil, mixerID)
	if err != nil {
		t.Fatalf("Failed to register rack: %v", err)
	}

	// Use the REAL MachineID assigned
	realIDGen, _ := idgen.New(rack.MachineID)
	rackEID := realIDGen.NewEntityID(idgen.EntityRack)

	// Seed a Snake
	snakeEID := idGen.NewEntityID(idgen.EntitySnake)
	mixerMachineID := uuid.New()
	_ = store.RegisterSnake(context.Background(), "snake-rack-1", snakeEID, "v1", rackEID, mixerID, "1.1.1.1", 1234, "2.2.2.2", 4222, mixerMachineID)

	// Verify we can find them
	foundRack, err := store.GetEntityIDByName(context.Background(), "rack-1")
	if err != nil {
		t.Fatalf("Failed to find rack: %v", err)
	}
	if foundRack[9] != rackEID[9] || foundRack[10] != rackEID[10] || foundRack[11] != rackEID[11] || foundRack[12] != rackEID[12] || foundRack[13] != rackEID[13] {
		t.Errorf("Failed to find rack (EID metadata mismatch): got %s, want metadata from %s", foundRack, rackEID)
	}
}
