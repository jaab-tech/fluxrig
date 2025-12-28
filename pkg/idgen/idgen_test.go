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
	extractedMachineID := uint16(id & 0xFFFF)
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
	extractedType := EntityType(eid >> 56)
	if extractedType != eType {
		t.Errorf("Type mismatch. Got %d, want %d", extractedType, eType)
	}

	// MachineID: Next 16 bits (55-40) => shift right 40, mask 0xFFFF
	extractedMachine := uint16((eid >> 40) & 0xFFFF)
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
