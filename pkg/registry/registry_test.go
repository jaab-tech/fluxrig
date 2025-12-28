package registry_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func setupTestRegistry(t *testing.T) (*registry.DuckDBRegistry, func()) {
	// Use unique temp file for isolation
	dbPath := t.TempDir() + "/fluxrig_test.duckdb"
	store, err := duckdb.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if err := store.InitializeSchema(context.Background()); err != nil {
		t.Fatalf("setup schema failed: %v", err)
	}

	return registry.NewDuckDBRegistry(store), func() { store.Close() }
}

func TestRegistry_Register_Static(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	// Register Static
	r, err := reg.Register(ctx, "lane-1", "", "10.0.0.1", 8080, "0.0.1")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if r.Name != "lane-1" {
		t.Errorf("Expected name lane-1, got %s", r.Name)
	}
	if r.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", r.Port)
	}
	if r.Stats == nil {
		t.Error("Stats should be initialized")
	}
	if r.Status != "active" {
		t.Errorf("Expected active, got %s", r.Status)
	}
	if r.MachineID == 0 {
		t.Error("ID should not be zero")
	}

	// Register Again (Idempotent update)
	// Must provide the secret established in the first call
	r2, err := reg.Register(ctx, "lane-1", r.Secret, "10.0.0.2", 9090, "0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if r2.MachineID != r.MachineID {
		t.Error("MachineID changed on re-register")
	}
	// Note: IP/Port are not persisted in Rack attributes (conceptually belong to Snake).
	// Removing assertions for IP/Port updates on Rack entity.
}

func TestRegistry_Register_ZeroConfig(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	// Register new worker (empty name)
	r, err := reg.Register(ctx, "", "", "192.168.1.50", 0, "")
	if err != nil {
		t.Fatal(err)
	}

	// Should be assigned "node-<ID>"
	if !contains(r.Name, "node-") {
		t.Errorf("Expected auto-name, got %s", r.Name)
	}
	if r.Status != "pending" {
		t.Errorf("Expected pending, got %s", r.Status)
	}

	// Dump DB
	rows, _ := reg.List(ctx, "")
	for _, x := range rows {
		t.Logf("Row: ID=%d Name=%s", x.MachineID, x.Name)
	}

	// Approve it
	t.Logf("Approving Rack %d to kitchen-disp-99", r.MachineID)
	newR, err := reg.Approve(ctx, r.MachineID, "kitchen-disp-99")
	if err != nil {
		t.Fatal(err)
	}

	if newR.Name != "kitchen-disp-99" {
		t.Errorf("Name not updated")
	}
	if newR.Status != "active" {
		t.Error("Status not set to active")
	}
}

func TestRegistry_List(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	_, _ = reg.Register(ctx, "a", "", "1.1.1.1", 1, "") // active
	_, _ = reg.Register(ctx, "", "", "2.2.2.2", 2, "")  // pending

	list, _ := reg.List(ctx, "")
	if len(list) != 2 {
		t.Errorf("Expected 2 racks, got %d", len(list))
	}

	pending, _ := reg.List(ctx, "pending")
	if len(pending) != 1 {
		t.Errorf("Expected 1 pending, got %d", len(pending))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[0:len(substr)] == substr
}

func TestRegistry_CRUD(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	// 1. Setup
	r, err := reg.Register(ctx, "crud-test", "", "10.10.10.10", 80, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Heartbeat (Success)
	stats := map[string]any{"cpu": 50}
	if err := reg.Heartbeat(ctx, r.MachineID, stats); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	// Verify Stats
	r2, _ := reg.Get(ctx, r.MachineID)
	if r2.Stats["cpu"].(float64) != 50 {
		// JSON numbers often unmarshal as float64 in generic maps
		t.Errorf("Stats mismatch: %v", r2.Stats)
	}

	// 3. Heartbeat (Fail - Not Found)
	if err := reg.Heartbeat(ctx, 9999, stats); err == nil {
		t.Error("Expected error for non-existent heartbeat")
	}

	// 4. UpdateStatus
	if err := reg.UpdateStatus(ctx, r.MachineID, "offline"); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
	r3, _ := reg.Get(ctx, r.MachineID)
	if r3.Status != "offline" {
		t.Errorf("Status mismatch: %s", r3.Status)
	}

	// 5. UpdateStatus (Fail)
	if err := reg.UpdateStatus(ctx, 9999, "offline"); err == nil {
		t.Error("Expected error update non-existent")
	}

	// 6. Remove
	if err := reg.Remove(ctx, r.MachineID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 7. Verify Gone
	if _, err := reg.Get(ctx, r.MachineID); err == nil {
		t.Error("Expected error getting removed rack")
	}

	// 8. Remove (Fail)
	if err := reg.Remove(ctx, 9999); err == nil {
		t.Error("Expected error removing non-existent")
	}
}
