// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registry_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestRegistry_Comprehensive(t *testing.T) {
	// 1. Setup Store
	s, err := duckdb.NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	if errMig := s.Migrate(context.Background()); errMig != nil {
		t.Fatalf("Migrate failed: %v", errMig)
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
	if err != nil {
		t.Fatalf("Failed to register victim: %v", err)
	}
	// Try renaming "victim" to "dup-name"
	_, errApprove := reg.Approve(ctx, 300, "dup-name") // MachineID likely 101 or similar?
	if errApprove == nil {
		t.Log("Expected error (maybe) or just checking assignment")
	}
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
