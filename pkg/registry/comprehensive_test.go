// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/registry/kvstore"
)

func TestRegistry_Comprehensive(t *testing.T) {
	s := kvstore.New(slog.Default(), kvstore.NewMemoryBucket(0), kvstore.NewMemoryBucket(time.Minute), nil, uuid.New(),
		kvstore.Config{MaxValueSize: 1 << 20, WriteRetries: 3})
	s.SetBootstrapSecret("rack")
	var err error

	var reg registry.Registry = s

	ctx := context.Background()
	testID := uuid.New()

	_, err = reg.Get(ctx, testID)
	if err == nil {
		t.Error("Get should fail for non-existent ID")
	}

	err = reg.Heartbeat(ctx, testID, map[string]any{"cpu": 1}, nil)
	if err == nil {
		t.Error("Heartbeat should fail for non-existent ID")
	}

	err = reg.UpdateStatus(ctx, testID, "inactive")
	if err == nil {
		t.Error("UpdateStatus should fail for non-existent ID")
	}

	mixerID := uuid.New()
	machineID1 := uuid.New()
	firstReg, err := reg.Register(ctx, machineID1, "dup-name", "rack", "1.2.3.4", 8080, "v1", map[string]any{"v": "v1"}, mixerID)
	if err != nil {
		t.Fatalf("First register failed: %v", err)
	}

	// Re-registering same machineID should NOT fail, using the secret that
	// registration actually issued: "rack" was only the one-time bootstrap
	// gate, and is never the identity's own secret from here on.
	_, err = reg.Register(ctx, machineID1, "dup-name", firstReg.Secret, "1.2.3.4", 8080, "v1", map[string]any{"v": "v1"}, mixerID)
	if err != nil {
		t.Errorf("Re-registering same machine should not fail: %v", err)
	}

	list, err := reg.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("Expected 1 item, got %d", len(list))
	}

	err = reg.Remove(ctx, uuid.New())
	if err == nil {
		t.Error("Remove should fail for non-existent ID")
	}

	machineID2 := uuid.New()
	victim, err := reg.Register(ctx, machineID2, "victim", "rack", "1.2.3.4", 9000, "v1", map[string]any{"v": "v1"}, mixerID)
	if err != nil {
		t.Fatalf("Failed to register victim: %v", err)
	}

	// Rename victim to duplicate name should fail
	_, err = reg.Approve(ctx, victim.MachineID, "dup-name")
	if err == nil {
		t.Error("Approve rename to existing name should fail")
	}

	// Telemetry queries go to the local store; with none attached they
	// report it rather than return nothing silently.
	_, err = reg.QueryLogs(ctx, registry.LogQuery{Limit: 1})
	if err == nil {
		t.Error("QueryLogs without a telemetry store should report it")
	}
}
