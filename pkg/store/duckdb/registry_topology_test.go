// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// A scenario registers several gears, ports and wires on the same machine.
// Every one of them must be stored: the registry holds one row per entity, not
// one row per kind of entity per machine.
func TestRegistry_SeveralEntitiesOnOneMachine(t *testing.T) {
	s, err := NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	machine, mixer, scenario := uuid.New(), uuid.New(), uuid.New()
	names := []string{"first", "second", "third"}
	gearIDs := map[string]uuid.UUID{}
	for _, n := range names {
		gearIDs[n] = uuid.New()
		inID, outID := uuid.New(), uuid.New()
		if err := s.RegisterPort(ctx, inID, n+".in", 6, gearIDs[n], scenario, machine, mixer); err != nil {
			t.Errorf("RegisterPort %s.in: %v", n, err)
		}
		if err := s.RegisterPort(ctx, outID, n+".out", 7, gearIDs[n], scenario, machine, mixer); err != nil {
			t.Errorf("RegisterPort %s.out: %v", n, err)
		}
		ports := map[string]uuid.UUID{"in": inID, "out": outID}
		if err := s.RegisterGear(ctx, gearIDs[n], n, "bento", "", "", "", scenario, machine, ports, mixer); err != nil {
			t.Errorf("RegisterGear %s: %v", n, err)
		}
	}
	for i := 0; i+1 < len(names); i++ {
		a, b := names[i], names[i+1]
		if err := s.RegisterWire(ctx, uuid.New(), a+".out", b+".in", uuid.New(), uuid.New(), scenario, machine, mixer); err != nil {
			t.Errorf("RegisterWire %s->%s: %v", a, b, err)
		}
	}

	var gears, ports, wires int
	_ = s.DB().QueryRowContext(ctx, "SELECT count(*) FROM registry WHERE type_id = 5").Scan(&gears)
	_ = s.DB().QueryRowContext(ctx, "SELECT count(*) FROM registry WHERE type_id IN (6, 7)").Scan(&ports)
	_ = s.DB().QueryRowContext(ctx, "SELECT count(*) FROM registry WHERE type_id = 8").Scan(&wires)
	if gears != 3 || ports != 6 || wires != 2 {
		t.Errorf("stored gears=%d ports=%d wires=%d, want 3, 6 and 2", gears, ports, wires)
	}
}

// A scenario imported again registers the same entities with the same ids.
func TestRegistry_RegisterAgainWithSameIDs(t *testing.T) {
	s, err := NewStore(slog.Default(), "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	machine, mixer, scenario := uuid.New(), uuid.New(), uuid.New()
	gear, in, out := uuid.New(), uuid.New(), uuid.New()
	for pass := 1; pass <= 2; pass++ {
		if err := s.RegisterPort(ctx, in, "g.in", 6, gear, scenario, machine, mixer); err != nil {
			t.Errorf("pass %d: RegisterPort in: %v", pass, err)
		}
		if err := s.RegisterPort(ctx, out, "g.out", 7, gear, scenario, machine, mixer); err != nil {
			t.Errorf("pass %d: RegisterPort out: %v", pass, err)
		}
		if err := s.RegisterGear(ctx, gear, "g", "bento", "", "", "", scenario, machine, map[string]uuid.UUID{"in": in, "out": out}, mixer); err != nil {
			t.Errorf("pass %d: RegisterGear: %v", pass, err)
		}
	}
}
