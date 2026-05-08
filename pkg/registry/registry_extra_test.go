// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRegistry_Approve_Errors(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()
	mixerID := uuid.New()

	// 1. Initial Reg (Pending)
	reg.SetAutoAdopt(false)
	id1 := uuid.New()
	p1, err := reg.Register(ctx, id1, "pending-1", "sec", "ip", 0, "v1", nil, mixerID)
	if err != nil {
		t.Fatalf("Setup register failed: %v", err)
	}

	// 2. Conflict: Same name, diff ID
	id2 := uuid.New()
	_, _ = reg.Register(ctx, id2, "taken", "sec", "ip", 0, "v1", nil, mixerID)

	if _, err := reg.Approve(ctx, p1.MachineID, "taken"); err == nil {
		t.Error("Expected conflict error")
	}

	// 4. Not Found
	if _, err := reg.Approve(ctx, uuid.New(), "new"); err == nil {
		t.Error("Expected not found")
	}
}

func TestRegistry_Register_Conflict(t *testing.T) {
	reg, teardown := setupTestRegistry(t)
	defer teardown()
	ctx := context.Background()
	mixerID := uuid.New()

	id := uuid.New()
	r1, err := reg.Register(ctx, id, "fixed", "secret1", "ip", 0, "v1", nil, mixerID)
	if err != nil {
		t.Fatalf("Setup register failed: %v", err)
	}

	// Re-register with wrong secret
	if _, err := reg.Register(ctx, id, "fixed", "wrong_secret", "ip", 0, "v1", nil, mixerID); err == nil {
		t.Error("Expected secret conflict error")
	}

	// Re-register with correct secret (Success)
	if _, err := reg.Register(ctx, id, "fixed", r1.Secret, "ip", 0, "v1", nil, mixerID); err != nil {
		t.Errorf("Re-register failed: %v", err)
	}
}
