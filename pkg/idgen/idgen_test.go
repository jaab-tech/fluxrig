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

package idgen

import (
	"fmt"
	"testing"
)

func TestIDGenerator_FluxID(t *testing.T) {
	machineID := uint16(10)
	gen, err := New(machineID)
	if err != nil {
		t.Fatalf("Failed to create generator: %v", err)
	}

	id, err := gen.NextFluxID()
	if err != nil {
		t.Fatalf("Failed to generate FluxID: %v", err)
	}

	// Verify Sonyflake structure (MachineID is lower 16 bits)
	// Actually Sonyflake default is:
	// 39 bits time | 8 bits seq | 16 bits machine
	// So lowest 16 bits should be machineID
	extractedMachineID := uint16(id & 0xFFFF) //nolint:gosec
	if extractedMachineID != machineID {
		t.Errorf("MachineID mismatch in FluxID. Got %d, want %d", extractedMachineID, machineID)
	}

	t.Logf("Generated FluxID: %d (Hex: %X)", id, id)
}

func TestIDGenerator_EntityID(t *testing.T) {
	machineID := uint16(55)
	gen, err := New(machineID)
	if err != nil {
		t.Fatalf("Failed to create generator: %v", err)
	}

	// Test case: Rack type, sequence 1
	eType := EntityRack
	seq := uint64(1)

	eid := gen.NewEntityID(eType, seq)

	// Verify bits
	// Type: Top 8 bits (63-56) => shift right 56
	extractedType := EntityType(eid >> 56) //nolint:gosec
	if extractedType != eType {
		t.Errorf("Type mismatch. Got %d, want %d", extractedType, eType)
	}

	// MachineID: Next 16 bits (55-40) => shift right 40, mask 0xFFFF
	extractedMachine := uint16((eid >> 40) & 0xFFFF) //nolint:gosec
	if extractedMachine != machineID {
		t.Errorf("MachineID mismatch. Got %d, want %d", extractedMachine, machineID)
	}

	// Sequence: Bottom 40 bits => mask 0xFFFFFFFFFF
	extractedSeq := eid & 0xFFFFFFFFFF
	if extractedSeq != seq {
		t.Errorf("Sequence mismatch. Got %d, want %d", extractedSeq, seq)
	}

	fmt.Printf("Entity ID (Rack, M:55, Seq:1): %X\n", eid)
}

func TestIDGenerator_NextEntityID(t *testing.T) {
	gen, _ := New(1)

	// Default starts at 0, first call -> 1
	id1 := gen.NextEntityID(EntityGear)
	if (id1 & 0xFFFFFFFFFF) != 1 {
		t.Errorf("Expected seq 1, got %d", id1&0xFFFFFFFFFF)
	}

	// Test SetSequence
	gen.SetSequence(100)
	id2 := gen.NextEntityID(EntityGear)
	if (id2 & 0xFFFFFFFFFF) != 101 {
		t.Errorf("Expected seq 101, got %d", id2&0xFFFFFFFFFF)
	}
}

func TestRandomSuffix(t *testing.T) {
	s1 := RandomSuffix(4)
	if len(s1) != 4 {
		t.Errorf("Expected len 4, got %d", len(s1))
	}
	s2 := RandomSuffix(8)
	if len(s2) != 8 {
		t.Errorf("Expected len 8, got %d", len(s2))
	}
	if s1 == s2 {
		t.Error("RandomSuffix returned duplicate (unlikely)")
	}
}
