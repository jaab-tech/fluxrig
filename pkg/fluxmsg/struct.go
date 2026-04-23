// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"encoding/hex"
	"fmt"
	"unicode/utf8"
)

// FluxMsg is the atomic unit of data in the system.
// Matches definition in docs/public/2_architecture/data.md
// and ops/docs/public/5_reference/protocols.md
const MetaCoatCheckTTL = "coatcheck.ttl"

type FluxMsg struct {
	// --- Identity & Tracing ---
	FluxID    uint64 `cbor:"id"`     // Global Unique ID (Sonyflake)
	RefFluxID uint64 `cbor:"ref_id"` // Correlation / Parent ID
	TraceID   string `cbor:"trace"`  // OpenTelemetry TraceID (Hex)
	SrcGearID uint64 `cbor:"src_id"` // Originator EntityID

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
	TsInit     int64  `cbor:"ts"`   // Timestamp of creation (Unix Nanos)
}

const (
	FlagSyncProbe uint32 = 1 << 0
)

// Hop represents a single processing step in recent history
type Hop struct {
	GearID uint64 `cbor:"g"`
	PortID uint64 `cbor:"p"`
	TsNano int64  `cbor:"t"`
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

// Validate ensures the message is bit-perfect for CBOR (UTF-8 keys/values).
func (m *FluxMsg) Validate() error {
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
