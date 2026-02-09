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

package fluxmsg

// FluxMsg is the atomic unit of data in the system.
// Matches definition in docs/public/2_architecture/data.md
// and ops/docs/public/5_reference/protocols.md
const MetaCoatCheckTTL = "coatcheck.ttl"

type FluxMsg struct {
	// --- Identity & Tracing ---
	FluxID    uint64 `msgpack:"id"`     // Global Unique ID (Sonyflake)
	RefFluxID uint64 `msgpack:"ref_id"` // Correlation / Parent ID
	TraceID   string `msgpack:"trace"`  // OpenTelemetry TraceID (Hex)
	SrcGearID uint64 `msgpack:"src_id"` // Originator EntityID

	// --- Context (Metadata) ---
	// Routing flags, source IP, protocol headers.
	Metadata map[string]string `msgpack:"meta"`

	// --- The Business Data ---
	// Unified Field Map (ISO8583 tags, JSON keys).
	Data map[string]any `msgpack:"data"`

	// --- Fallback & Audit ---
	RawPayload []byte `msgpack:"raw"`  // Original wire bytes
	Path       []*Hop `msgpack:"path"` // Audit Trail
	TsInit     int64  `msgpack:"ts"`   // Timestamp of creation (Unix Nanos)
}

// Hop represents a single processing step in recent history
type Hop struct {
	GearID uint64 `msgpack:"g"`
	PortID uint64 `msgpack:"p"`
	TsNano int64  `msgpack:"t"`
}

// New creates a basic FluxMsg with initialized maps to avoid nil panics
func New() *FluxMsg {
	return &FluxMsg{
		Metadata: make(map[string]string),
		Data:     make(map[string]any),
		Path:     make([]*Hop, 0),
	}
}
