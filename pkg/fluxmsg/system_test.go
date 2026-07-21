// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestSystemPayloads(t *testing.T) {
	machineID := uuid.New()

	t.Run("HelloPayload", func(t *testing.T) {
		orig := &HelloPayload{
			Name:      "test-rack",
			Nonce:     "12345",
			MachineID: machineID,
			Secret:    "secret",
			IP:        "127.0.0.1",
			Port:      8080,
			Version:   "1.0.0",
			Config:    map[string]any{"key": "value"},
		}

		data, err := orig.ToData()
		if err != nil {
			t.Fatalf("ToData failed: %v", err)
		}

		parsed, err := ParseHello(data)
		if err != nil {
			t.Fatalf("ParseHello failed: %v", err)
		}

		if parsed.Name != orig.Name || parsed.Nonce != orig.Nonce || parsed.MachineID != orig.MachineID {
			t.Errorf("Mismatch in HelloPayload: got %+v", parsed)
		}
	})

	t.Run("HeartbeatPayload", func(t *testing.T) {
		orig := &HeartbeatPayload{
			MachineID: machineID,
			Stats:     map[string]any{"cpu": uint64(50)},
			Config:    map[string]any{"up": true},
		}

		data, err := orig.ToData()
		if err != nil {
			t.Fatalf("ToData failed: %v", err)
		}

		parsed, err := ParseHeartbeat(data)
		if err != nil {
			t.Fatalf("ParseHeartbeat failed: %v", err)
		}

		if parsed.MachineID != orig.MachineID {
			t.Errorf("Mismatch in HeartbeatPayload")
		}
	})

	t.Run("HelloResponse", func(t *testing.T) {
		orig := &HelloResponse{
			Status:   "active",
			Passport: []byte("pass"),
			Config:   map[string]string{"k": "v"},
			Message:  "ok",
		}

		data, err := orig.ToData()
		if err != nil {
			t.Fatalf("ToData failed: %v", err)
		}

		parsed, err := ParseHelloResponse(data)
		if err != nil {
			t.Fatalf("ParseHelloResponse failed: %v", err)
		}

		if parsed.Status != orig.Status || string(parsed.Passport) != string(orig.Passport) {
			t.Errorf("Mismatch in HelloResponse")
		}
	})

	t.Run("HeartbeatResponse", func(t *testing.T) {
		orig := &HeartbeatResponse{
			Status:   "active",
			Command:  "sleep",
			Passport: []byte("pass2"),
		}

		data, err := orig.ToData()
		if err != nil {
			t.Fatalf("ToData failed: %v", err)
		}

		parsed, err := ParseHeartbeatResponse(data)
		if err != nil {
			t.Fatalf("ParseHeartbeatResponse failed: %v", err)
		}

		if parsed.Command != orig.Command {
			t.Errorf("Mismatch in HeartbeatResponse")
		}
	})

	t.Run("ScenarioPayload", func(t *testing.T) {
		orig := &ScenarioPayload{
			Version:   "v1",
			Name:      "test-scenario",
			RackName:  "rack-1",
			MachineID: machineID,
			Scenario:  []byte("yaml data"),
			Timestamp: 123456789,
		}

		data, err := orig.ToData()
		if err != nil {
			t.Fatalf("ToData failed: %v", err)
		}

		parsed, err := ParseScenarioPayload(data)
		if err != nil {
			t.Fatalf("ParseScenarioPayload failed: %v", err)
		}

		if parsed.Name != orig.Name || !reflect.DeepEqual(parsed.Scenario, orig.Scenario) {
			t.Errorf("Mismatch in ScenarioPayload")
		}
	})
}

func TestFromMapErrors(t *testing.T) {
	// Provide un-marshalable map
	invalidData := map[string]any{
		"machine_id": func() {}, // Functions cannot be marshaled by CBOR
	}

	_, err := ParseHello(invalidData)
	if err == nil {
		t.Error("expected error on invalid map")
	}

	_, err = ParseHeartbeat(invalidData)
	if err == nil {
		t.Error("expected error on invalid map")
	}

	_, err = ParseHelloResponse(invalidData)
	if err == nil {
		t.Error("expected error on invalid map")
	}

	_, err = ParseHeartbeatResponse(invalidData)
	if err == nil {
		t.Error("expected error on invalid map")
	}

	_, err = ParseScenarioPayload(invalidData)
	if err == nil {
		t.Error("expected error on invalid map")
	}
}
