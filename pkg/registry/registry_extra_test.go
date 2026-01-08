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
	"testing"
)

func TestRegistry_Approve_Errors(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	// 1. Initial Reg (Pending)
	p1, err := reg.Register(ctx, "", "sec", "ip", 0, "v1", nil, 1)
	if err != nil {
		t.Fatalf("Setup register failed: %v", err)
	}
	if p1 == nil {
		t.Fatal("Register returned nil rack")
	}

	// 2. Conflict: Same name, diff ID
	_, _ = reg.Register(ctx, "taken", "sec", "ip", 0, "v1", nil, 2)

	if _, err := reg.Approve(ctx, p1.MachineID, "taken"); err == nil {
		t.Error("Expected conflict error")
	}

	// 3. Self-Rename (Should allow if logic supports it, though usually pending -> active name change)
	// If "taken" tries to approve as "taken", it's fine.

	// 4. Not Found
	if _, err := reg.Approve(ctx, 9999, "new"); err == nil {
		t.Error("Expected not found")
	}
}

func TestRegistry_Register_Conflict(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()

	r1, err := reg.Register(ctx, "fixed", "secret1", "ip", 0, "v1", nil, 1)
	if err != nil {
		t.Fatalf("Setup register failed: %v", err)
	}

	// Re-register with wrong secret
	if _, err := reg.Register(ctx, "fixed", "wrong_secret", "ip", 0, "v1", nil, 1); err == nil {
		t.Error("Expected secret conflict error")
	}

	// Re-register with correct secret (Success)
	if _, err := reg.Register(ctx, "fixed", r1.Secret, "ip", 0, "v1", nil, 1); err != nil {
		t.Errorf("Re-register failed: %v", err)
	}
}
