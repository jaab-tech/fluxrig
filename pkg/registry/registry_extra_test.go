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
	p1, _ := reg.Register(ctx, "", "sec", "ip", 0, "v1", nil, 1)

	// 2. Conflict: Same name, diff ID
	reg.Register(ctx, "taken", "sec", "ip", 0, "v1", nil, 2)

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

	r1, _ := reg.Register(ctx, "fixed", "secret1", "ip", 0, "v1", nil, 1)

	// Re-register with wrong secret
	if _, err := reg.Register(ctx, "fixed", "wrong_secret", "ip", 0, "v1", nil, 1); err == nil {
		t.Error("Expected secret conflict error")
	}

	// Re-register with correct secret (Success)
	if _, err := reg.Register(ctx, "fixed", r1.Secret, "ip", 0, "v1", nil, 1); err != nil {
		t.Errorf("Re-register failed: %v", err)
	}
}
