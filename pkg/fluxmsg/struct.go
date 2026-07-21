// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

// FluxMsg is the atomic unit of data in the system.
// Matches definition in docs/public/2_architecture/data.md
// and ops/docs/public/5_reference/protocols.md
const MetaCoatCheckTTL = "coatcheck.ttl"

type FluxMsg struct {
	// --- Identity & Tracing ---
	FluxID    uuid.UUID `cbor:"id"`     // Global Unique ID (UUID v7)
	RefFluxID uuid.UUID `cbor:"ref_id"` // Correlation / Parent ID
	TraceID   string    `cbor:"trace"`  // OpenTelemetry TraceID (Hex)
	SrcGearID uuid.UUID `cbor:"src_id"` // Originator EntityID

	// --- Context (Metadata) ---
	// Routing flags, source IP, protocol headers.
	Metadata map[string]string `cbor:"meta"`

	// --- The Business Data ---
	// Unified Field Map (ISO8583 tags, JSON keys).
	Data  map[string]any `cbor:"data"`
	Flags uint32         `cbor:"flags"` // System-level signaling (0x01 = Sync Probe)

	// --- Fallback & Audit ---
	RawPayload []byte `cbor:"raw"`  // Original wire bytes
	Path       []*Hop `cbor:"path"` // Audit Trail
	TSInit     int64  `cbor:"ts"`   // Timestamp of creation (Unix Nanos)
}

const (
	FlagSyncProbe uint32 = 1 << 0
)

var (
	// MaxHops is the maximum number of hops a message can take before being discarded.
	// Prevents infinite routing loops. Defaults to 64, can be set via SetLimits().
	MaxHops int = 64
	// MaxPayloadSize is the maximum size (in bytes) of a message's core components.
	// Prevents memory exhaustion attacks. Defaults to 2MB, can be set via SetLimits().
	MaxPayloadSize int = 2 * 1024 * 1024
)

// SetLimits configures the global message validation boundaries.
// This should be called once on startup by the application (Rack/Mixer).
func SetLimits(maxHops, maxPayloadSize int) {
	MaxHops = maxHops
	MaxPayloadSize = maxPayloadSize
}

// Hop represents a single processing step in recent history
type Hop struct {
	GearID uuid.UUID `cbor:"g"`
	PortID uuid.UUID `cbor:"p"`
	TSNano int64     `cbor:"t"`
}

// SetMetadata sets a value in the metadata map, automatically hex-encoding
// non-UTF8 strings to ensure CBOR compatibility.
func (m *FluxMsg) SetMetadata(k, v string) {
	if utf8.ValidString(v) {
		m.Metadata[k] = v
	} else {
		m.Metadata[k] = fmt.Sprintf("hex:%s", hex.EncodeToString([]byte(v)))
	}
}

// SetMetadataBytes sets a binary value in the metadata map as a hex-encoded string.
func (m *FluxMsg) SetMetadataBytes(k string, v []byte) {
	m.Metadata[k] = fmt.Sprintf("hex:%s", hex.EncodeToString(v))
}

// Validate ensures the message is bit-perfect for CBOR (UTF-8 keys/values)
// and adheres to resource limits (Hops, Size).
func (m *FluxMsg) Validate() error {
	// 1. Resource Limits: Hops
	if len(m.Path) > MaxHops {
		return fmt.Errorf("message exceeded maximum hops: %d > %d", len(m.Path), MaxHops)
	}

	// 2. Resource Limits: Size (Heuristic check of core components)
	// We check Metadata, Data keys/values, and RawPayload.
	currentSize := len(m.RawPayload)
	for k, v := range m.Metadata {
		currentSize += len(k) + len(v)
	}
	for k, v := range m.Data {
		currentSize += len(k)
		if s, ok := v.(string); ok {
			currentSize += len(s)
		} else if b, ok := v.([]byte); ok {
			currentSize += len(b)
		}
		// Note: We don't deep-scan complex types (maps/slices) here to keep validation fast.
		// Standard CBOR unmarshaling will hit memory limits anyway, but this catches
		// oversized simple payloads early.
	}

	if currentSize > MaxPayloadSize {
		return fmt.Errorf("message exceeded maximum payload size: %d > %d", currentSize, MaxPayloadSize)
	}

	// 3. UTF-8 Validation
	for k, v := range m.Metadata {
		if !utf8.ValidString(k) {
			return fmt.Errorf("invalid UTF-8 in metadata key: %q", k)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("invalid UTF-8 in metadata value for key %q", k)
		}
	}
	// Deep validation for Data map (string keys only)
	for k := range m.Data {
		if !utf8.ValidString(k) {
			return fmt.Errorf("invalid UTF-8 in data key: %q", k)
		}
	}
	return nil
}

// New creates a basic FluxMsg with initialized maps to avoid nil panics
func New() *FluxMsg {
	return &FluxMsg{
		Metadata: make(map[string]string),
		Data:     make(map[string]any),
		Path:     make([]*Hop, 0),
	}
}
