// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package idgen

import (
	"crypto/rand"
	"fmt"

	"github.com/google/uuid"
)

// randRead is the entropy seam. It is crypto/rand.Read in production, and a
// test can replace it to drive the failure path of RandomSuffix, the same way
// uuid.SetRand drives the failure path of NewEntityID.
var randRead = rand.Read

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
//
// The hint is the low 32 bits of the machine ID, a hint and not an identity.
// A v7 UUID's last 32 bits are random, so the hint is a 32-bit random value,
// and two different machine IDs can carry the same hint. The birthday bound
// puts a 50% chance of any collision at about 65536 machines; a fleet of m
// machines has a collision probability near m^2 / 2^33. The hint is for fast
// filtering and human debugging, never for authentication or uniqueness: the
// full 128-bit ID stays unique because the timestamp and the remaining random
// bits differ, and at the fleet sizes this project targets the risk is far
// below the v7 timestamp/random collision bound.
func (g *IDGenerator) NewEntityID(etype EntityType) uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// The only way this fails is the entropy source itself failing
		// (crypto/rand), which is fatal for anything else relying on it
		// too. Silently falling through would have built an entity ID
		// from a zero-value UUID plus whatever machine-id hint follows,
		// not a real V7 ID.
		panic(fmt.Sprintf("idgen: failed to generate a V7 UUID: %v", err))
	}
	bytes := id

	// We preserve the first 48 bits (Timestamp) and the 4 bits of Version.
	// We then inject our metadata into the remaining bits.

	// To keep it simple and safe, we'll use bytes 9-12 for a MachineID hint (32 bits)
	// and byte 13 for EntityType (8 bits).
	// We take the last 4 bytes of the machineID as the hint.
	//
	// A Nil machine ID is a real, expected state here, not a caller error:
	// a Rack that has not enrolled yet (the Zero Config bootstrap)
	// legitimately has no machine ID assigned. There is nothing meaningful
	// to hint at in that case, so the random bytes uuid.NewV7 already put
	// in this region are left alone instead of being overwritten with a
	// Nil-derived, all-zero hint that every not-yet-enrolled generator
	// would then share.
	if g.machineID != uuid.Nil {
		copy(bytes[9:13], g.machineID[12:16])
	}
	bytes[13] = uint8(etype)

	return uuid.UUID(bytes)
}

// RandomSuffix generates a random hex suffix of the specified length. It
// returns the entropy error rather than a placeholder: a silent fallback would
// hand the caller a plausible, wrong suffix with no signal. A caller that
// needs a suffix must not use one that is not random, so an error it can act
// on is the only safe contract.
func RandomSuffix(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	b := make([]byte, (n+1)/2)
	if _, err := randRead(b); err != nil {
		return "", fmt.Errorf("idgen: random suffix: %w", err)
	}
	return fmt.Sprintf("%x", b)[:n], nil
}
