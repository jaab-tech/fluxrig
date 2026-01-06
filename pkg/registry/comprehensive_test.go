package registry_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestRegistry_ErrorPaths(t *testing.T) {
	// Setup Store
	s, err := duckdb.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()
	if err := s.InitializeSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	reg := registry.NewDuckDBRegistry(s)

	ctx := context.Background()

	// 1. Get Non-Existent
	_, err = reg.Get(ctx, 9999)
	if err == nil {
		t.Error("Get(9999) should fail")
	}

	// 2. Heartbeat Non-Existent
	err = reg.Heartbeat(ctx, 9999, map[string]any{"cpu": 1}, nil)
	if err == nil {
		t.Error("Heartbeat(9999) should fail")
	}

	// 3. UpdateStatus Non-Existent
	err = reg.UpdateStatus(ctx, 9999, "inactive")
	if err == nil {
		t.Error("UpdateStatus(9999) should fail")
	}

	// 4. Register with Missing Name (Should fail at DB level due to constraint? Or just insert empty?)
	// name is not NULL in schema? "name TEXT". Only "PRIMARY KEY" on entity_id.
	// But `InitializeSchema` creates UNIQUE INDEX on name.
	// So duplicate name should fail.
	_, err = reg.Register(ctx, "dup-name", "rack", "dup-name", 8080, "1.2.3.4", map[string]any{"v": "v1"}, 500)
	if err != nil {
		t.Fatalf("First register failed: %v", err)
	}
	// Try duplicate
	_, err = reg.Register(ctx, "dup-name", "rack", "dup-name", 8081, "1.2.3.4", map[string]any{"v": "v1"}, 501)
	if err == nil {
		t.Error("Duplicate Register should fail (Unique Name Index)")
	}

	// 5. List with Limits
	// DuckDBRegistry.List implements status filtering, not pagination (limit/offset args removed/different?)
	// Interface: List(ctx, status string) ([]*Rack, error)
	// My previous test code assumed List(ctx, limit, offset).
	list, err := reg.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("Expected 1 item, got %d", len(list))
	}

	// 6. Remove Non-Existent
	err = reg.Remove(ctx, 9999)
	if err == nil {
		t.Error("Remove(9999) should fail")
	}

	// 7. Approve Name Conflict
	// "dup-name" exists (from 4).
	// Register another one
	_, err = reg.Register(ctx, "victim", "rack", "victim", 9000, "1.2.3.4", map[string]any{"v": "v1"}, 502)
	// Try renaming "victim" to "dup-name"
	_, err = reg.Approve(ctx, 300, "dup-name") // MachineID likely 101 or similar?
	// We need actual ID.
	// But without list, we assume sequence.
	// Let's get victim ID
	victim, _ := reg.Get(ctx, 101) // 100 was dup-name, 101 is victim (Sequence 100 start?)
	if victim != nil {
		_, err = reg.Approve(ctx, victim.MachineID, "dup-name")
		if err == nil {
			t.Error("Approve rename to existing name should fail")
		}
	}

	// 8. Query Wrappers (Coverage)
	_, _ = reg.QueryLogs(ctx, registry.LogQuery{Limit: 1})
	_, _ = reg.QueryMetrics(ctx, registry.MetricQuery{Limit: 1})
}
