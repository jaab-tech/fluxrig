package idgen

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/sony/sonyflake"
	"sync/atomic"
)

// IDGenerator provides unique IDs for messages (fluxID) and components (fluxEntityID).
// Reference: ops/docs/public/5_reference/protocols.md
type IDGenerator struct {
	sf        *sonyflake.Sonyflake
	machineID uint16
	seq       atomic.Uint64 // Monotonic counter for EntityIDs
}

// EntityType defines the type prefix for fluxEntityID
type EntityType uint8

// Entity Types from protocols.md
// NOTE: Entity type IDs are fixed and must not change after v1.0 release.
// Cluster and Mixer are top-level hierarchy for multi-tenant support.
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

// EntityTypes maps ID to human readable name
var EntityTypes = map[EntityType]string{
	EntityReserved:    "Reserved",
	EntityCluster:     "Cluster",
	EntityMixer:       "Mixer",
	EntityFluxMsg:     "FluxMsg",
	EntityRack:        "Rack",
	EntityGear:        "Gear",
	EntityPortInput:   "PortInput",
	EntityPortOutput:  "PortOutput",
	EntityWire:        "Wire",
	EntitySnake:       "Snake",
	EntityScenario:    "Scenario",
	EntitySession:     "Session",
	EntitySpecVersion: "SpecVersion",
}

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

// NextEntityID generates a persistent/runtime Component ID using an internal counter.
// Supports runtime entities (like Sessions) by incrementing the sequence.
func (g *IDGenerator) NextEntityID(etype EntityType) uint64 {
	// Increment sequence
	seq := g.seq.Add(1)
	return g.NewEntityID(etype, seq)
}

// NewEntityID constructs a persistent Component ID from a given sequence.
// Structure: [ Type (8) ] [ MachineID (16) ] [ Local Sequence (40) ]
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

// SetSequence manually sets the internal sequence counter.
// Used for resuming after a restart by loading the max sequence from DB.
func (g *IDGenerator) SetSequence(seq uint64) {
	g.seq.Store(seq)
}

// RandomSuffix generates a random hex suffix of the specified length.
func RandomSuffix(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return "0000" // Fallback
	}
	return fmt.Sprintf("%x", b)[:n]
}
