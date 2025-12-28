package idgen

import (
	"errors"
	"time"

	"github.com/sony/sonyflake"
)

// IDGenerator provides unique IDs for messages (fluxID) and components (fluxEntityID).
// Reference: ops/docs/public/5_reference/protocols.md
type IDGenerator struct {
	sf        *sonyflake.Sonyflake
	machineID uint16
}

// EntityType defines the type prefix for fluxEntityID
type EntityType uint8

// Entity Types from protocols.md
const (
	EntityReserved    EntityType = 0x00
	EntityFluxMsg     EntityType = 0x01
	EntityMixer       EntityType = 0x02
	EntityRack        EntityType = 0x03
	EntityGear        EntityType = 0x04
	EntityPortInput   EntityType = 0x05
	EntityPortOutput  EntityType = 0x06
	EntityWire        EntityType = 0x07
	EntitySnake       EntityType = 0x08
	EntityScenario    EntityType = 0x09
	EntitySession     EntityType = 0x0A
	EntitySpecVersion EntityType = 0x0B
)

// New creates a new IDGenerator fixed to a specific MachineID.
// In a Rack, this MachineID must be unique per instance.
func New(machineID uint16) (*IDGenerator, error) {
	settings := sonyflake.Settings{
		StartTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), // Project Epoch
		MachineID: func() (uint16, error) {
			return machineID, nil
		},
	}

	sf := sonyflake.NewSonyflake(settings)
	if sf == nil {
		return nil, errors.New("failed to initialize sonyflake")
	}

	return &IDGenerator{
		sf:        sf,
		machineID: machineID,
	}, nil
}

// NextFluxID generates a standard k-sortable unique ID (Sonyflake).
// Structure: [ Timestamp (39) ] [ Sequence (8) ] [ MachineID (16) ]
func (g *IDGenerator) NextFluxID() (uint64, error) {
	return g.sf.NextID()
}

// NextEntityID constructs a persistent Component ID.
// Structure: [ Type (8) ] [ MachineID (16) ] [ Local Sequence (40) ]
// The 'localSequence' MUST be derived from a persistent counter to ensure uniqueness.
func (g *IDGenerator) NewEntityID(etype EntityType, localSequence uint64) uint64 {
	// Shift and combine
	// Type: Top 8 bits (63-56)
	// MachineID: Next 16 bits (55-40)
	// Sequence: Bottom 40 bits (39-0)

	// Mask sequence to 40 bits to be safe
	seqMasked := localSequence & 0xFFFFFFFFFF

	id := (uint64(etype) << 56) |
		(uint64(g.machineID) << 40) |
		seqMasked

	return id
}
