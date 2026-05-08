// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func setupTestRegistry(t *testing.T) (registry.Registry, func()) {
	store, err := duckdb.NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	if err := store.Migrate(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("Migrate failed: %v", err)
	}

	store.SetAutoAdopt(true)
	return store, func() { _ = store.Close() }
}

func TestRegistry_Register_Static(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()
	mixerID := uuid.New()
	machineID := uuid.New()

	r, err := reg.Register(ctx, machineID, "lane-1", "rack", "lane-1", 8080, "v1", map[string]any{"version": "0.0.1"}, mixerID)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if r.Name != "lane-1" {
		t.Errorf("Expected name lane-1, got %s", r.Name)
	}
	if r.Status != "active" {
		t.Errorf("Expected active, got %s", r.Status)
	}
	if r.MachineID != machineID {
		t.Error("MachineID mismatch")
	}

	r2, err := reg.Register(ctx, machineID, "lane-1", r.Secret, "lane-1", 9090, "v1", map[string]any{"version": "0.0.2"}, mixerID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.MachineID != r.MachineID {
		t.Error("MachineID changed on re-register")
	}
}

func TestRegistry_Register_ZeroConfig(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	_, _ = reg.Register(ctx, uuid.New(), "", "rack", "", 0, "v1", nil, uuid.New())
}

func TestRegistry_List(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()
	mixerID := uuid.New()

	_, _ = reg.Register(ctx, uuid.New(), "a", "rack", "a", 1, "v1", nil, mixerID)
	_, _ = reg.Register(ctx, uuid.New(), "b", "rack", "b", 2, "v1", nil, mixerID)

	list, err := reg.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Expected 2 items, got %d", len(list))
	}
}

func TestRegistry_CRUD(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()
	mixerID := uuid.New()
	machineID := uuid.New()

	r, err := reg.Register(ctx, machineID, "crud-test", "rack", "crud-test", 80, "v1", map[string]any{"version": "1.0.0"}, mixerID)
	if err != nil {
		t.Fatal(err)
	}

	stats := map[string]any{"cpu": 50.0}
	if err := reg.Heartbeat(ctx, r.MachineID, stats, nil); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	r2, _ := reg.Get(ctx, r.MachineID)
	if r2.Stats["cpu"].(float64) != 50.0 {
		t.Errorf("Stats mismatch: %v", r2.Stats)
	}

	if err := reg.Heartbeat(ctx, uuid.New(), stats, nil); err == nil {
		t.Error("Expected error for non-existent heartbeat")
	}

	if err := reg.UpdateStatus(ctx, r.MachineID, "offline"); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
	r3, _ := reg.Get(ctx, r.MachineID)
	if r3.Status != "offline" {
		t.Errorf("Status mismatch: %s", r3.Status)
	}

	if err := reg.UpdateStatus(ctx, uuid.New(), "offline"); err == nil {
		t.Error("Expected error update non-existent")
	}

	if err := reg.Remove(ctx, r.MachineID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if _, err := reg.Get(ctx, r.MachineID); err == nil {
		t.Error("Expected error getting removed rack")
	}

	if err := reg.Remove(ctx, uuid.New()); err == nil {
		t.Error("Expected error removing non-existent")
	}
}
