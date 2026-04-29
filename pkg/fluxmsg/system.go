// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// System Subjects
const (
	SubjectAgentHello     = "fluxrig.agent.hello"
	SubjectAgentHeartbeat = "fluxrig.agent.heartbeat"
	// SubjectScenarioUpdate is published by Mixer to push scenarios to specific racks
	// Topic pattern: fluxrig.rack.{rack_name}.scenario
	SubjectScenarioPrefix = "fluxrig.rack."
	SubjectScenarioSuffix = ".scenario"
)

// HelloPayload is sent by the Rack Agent on startup.
type HelloPayload struct {
	Name      string         `cbor:"name"`
	Nonce     string         `cbor:"nonce"` // Unique session nonce for response topic isolation
	MachineID uint16         `cbor:"machine_id"`
	Secret    string         `cbor:"secret"` // Bearer Token (Optional on first connect)
	IP        string         `cbor:"ip"`
	Port      int            `cbor:"port"`
	Version   string         `cbor:"version"`
	Config    map[string]any `cbor:"config"`
}

// HeartbeatPayload is sent periodically by the Rack.
type HeartbeatPayload struct {
	MachineID uint16         `cbor:"machine_id"`
	Stats     map[string]any `cbor:"stats"`
	Config    map[string]any `cbor:"config"`
}

// ToData converts the struct to a map[string]any for FluxMsg.Data.
// We use CBOR round-trip to respect tags and types.
func (h *HelloPayload) ToData() (map[string]any, error) {
	return toMap(h)
}

func (h *HeartbeatPayload) ToData() (map[string]any, error) {
	return toMap(h)
}

// ParseHello extracts HelloPayload from FluxMsg.Data
func ParseHello(data map[string]any) (*HelloPayload, error) {
	var h HelloPayload
	if err := fromMap(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// ParseHeartbeat extracts HeartbeatPayload from FluxMsg.Data
func ParseHeartbeat(data map[string]any) (*HeartbeatPayload, error) {
	var h HeartbeatPayload
	if err := fromMap(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// HelloResponse is sent by the Mixer to the Rack.
type HelloResponse struct {
	Status   string            `cbor:"status"`   // active, pending, inactive
	Passport []byte            `cbor:"passport"` // Null if no new passport
	Config   map[string]string `cbor:"config"`   // Dynamic Config (Optional)
	Message  string            `cbor:"message"`  // Human readable status message
}

// HeartbeatResponse is sent by the Mixer to the Rack.
type HeartbeatResponse struct {
	Status   string `cbor:"status"`   // active, pending, inactive
	Command  string `cbor:"command"`  // e.g., "reconnect", "sleep"
	Passport []byte `cbor:"passport"` // Updated identity (optional)
}

func (h *HelloResponse) ToData() (map[string]any, error) {
	return toMap(h)
}

func (h *HeartbeatResponse) ToData() (map[string]any, error) {
	return toMap(h)
}

// ParseHelloResponse parses the response from Mixer
func ParseHelloResponse(data map[string]any) (*HelloResponse, error) {
	var h HelloResponse
	if err := fromMap(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func ParseHeartbeatResponse(data map[string]any) (*HeartbeatResponse, error) {
	var h HeartbeatResponse
	if err := fromMap(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Helper generic converters using CBOR instead of JSON
func toMap(v any) (map[string]any, error) {
	b, err := cbor.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := cbor.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func fromMap(m map[string]any, v any) error {
	b, err := cbor.Marshal(m)
	if err != nil {
		return err
	}
	if err := cbor.Unmarshal(b, v); err != nil {
		return fmt.Errorf("failed to parse payload: %w", err)
	}
	return nil
}

// ScenarioPayload is sent by the Mixer to push scenario updates to Racks.
type ScenarioPayload struct {
	Version   string `cbor:"version"`    // Scenario version
	Name      string `cbor:"name"`       // Scenario name
	RackName  string `cbor:"rack_name"`  // Target rack name
	MachineID uint16 `cbor:"machine_id"` // Target machine ID
	Scenario  []byte `cbor:"scenario"`   // YAML-encoded scenario (projected for this rack)
	Timestamp int64  `cbor:"timestamp"`  // Unix timestamp
}

func (s *ScenarioPayload) ToData() (map[string]any, error) {
	return toMap(s)
}

// ParseScenarioPayload extracts ScenarioPayload from FluxMsg.Data
func ParseScenarioPayload(data map[string]any) (*ScenarioPayload, error) {
	var s ScenarioPayload
	if err := fromMap(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
