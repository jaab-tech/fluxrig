// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package idgen

import (
	"crypto/rand"
	"fmt"

	"github.com/google/uuid"
)

// IDGenerator provides unique IDs for messages (`flux_id`) and components (`entity_id`).
// v0.4.6 Migration: Now uses UUID v7 (RFC 9562) for 128-bit time-ordered sovereignty.
type IDGenerator struct {
	machineID uuid.UUID
}

// EntityType defines the type prefix for `entity_id`
type EntityType uint8

const (
	EntityReserved    EntityType = 0x00 // System Broadcast / Null
	EntityCluster     EntityType = 0x01 // Physical infrastructure (HA cluster)
	EntityMixer       EntityType = 0x02 // Logical tenant unit (virtual mixer)
	EntityFluxMsg     EntityType = 0x03 // Message Instance
	EntityRack        EntityType = 0x04 // Edge compute node
	EntityGear        EntityType = 0x05 // Processing unit
	EntityPortInput   EntityType = 0x06 // Sink (Standard In)
	EntityPortOutput  EntityType = 0x07 // Source (Standard Out)
	EntityWire        EntityType = 0x08 // Connection between ports
	EntitySnake       EntityType = 0x09 // Transport tunnel
	EntityScenario    EntityType = 0x0A // Config definition
	EntitySession     EntityType = 0x0B // Runtime context
	EntitySpecVersion EntityType = 0x0C // Immutable spec blob ID
)

// New creates a new IDGenerator fixed to a specific machine_id.
func New(machineID uuid.UUID) (*IDGenerator, error) {
	return &IDGenerator{
		machineID: machineID,
	}, nil
}

// NextFluxID generates a standard UUID v7 (Time-ordered).
func (g *IDGenerator) NextFluxID() (uuid.UUID, error) {
	// For messages, we use a standard V7 with full randomness for maximum entropy.
	return uuid.NewV7()
}

// NextEntityID generates a persistent/runtime Component ID.
func (g *IDGenerator) NextEntityID(etype EntityType) uuid.UUID {
	return g.NewEntityID(etype)
}

// NewEntityID constructs a persistent Component ID using UUID v7 layout
// but embeds EntityType and machine_id hint in the random/sequence bits.
// Layout: [ Timestamp (48) ] [ Version (4) ] [ Variant (2) ] [ Type (8) ] [ machine_id Hint (32) ] [ Rand (34) ]
func (g *IDGenerator) NewEntityID(etype EntityType) uuid.UUID {
	id, _ := uuid.NewV7()
	bytes := id

	// We preserve the first 48 bits (Timestamp) and the 4 bits of Version.
	// We then inject our metadata into the remaining bits.

	// To keep it simple and safe, we'll use bytes 9-12 for a MachineID hint (32 bits)
	// and byte 13 for EntityType (8 bits).
	// We take the last 4 bytes of the machineID as the hint.
	mHint := g.machineID[12:16]
	copy(bytes[9:13], mHint)
	bytes[13] = uint8(etype)

	return uuid.UUID(bytes)
}

// RandomSuffix generates a random hex suffix of the specified length.
func RandomSuffix(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return "0000" // Fallback
	}
	return fmt.Sprintf("%x", b)[:n]
}
