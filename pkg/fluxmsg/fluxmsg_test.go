// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// Validation Note:
// To validate the Hex output externally, you can specific MsgPack tools or online inspectors
// (e.g., https://msgpack.org/ -> Javascript demo or similar hex-to-msgpack converters).
// The structure matches 'ops/docs/public/5_reference/protocols.md'.

func TestFluxMsg_Scenarios(t *testing.T) {
	tests := []struct {
		name  string
		setup func() *FluxMsg
	}{
		{
			name: "Minimal (Identity Only)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = 1696098000000000123 // Example Sonyflake ID
				msg.TsInit = time.Now().UnixNano()
				return msg
			},
		},
		{
			name: "Standard Payment (ISO8583-like)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = 2000000000000000001
				msg.TraceID = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" // W3C format

				// Standard Metadata keys from protocols.md
				msg.Metadata["iso.mti"] = "0200"
				msg.Metadata["peer.ip"] = "10.0.0.5"

				// Business Data
				if err := msg.Set("pan_masked", "4111xxxxxxxx1111"); err != nil {
					panic(err)
				}
				if err := msg.Set("amount", 1500.50); err != nil {
					panic(err)
				}
				if err := msg.Set("currency", 840); err != nil {
					panic(err)
				}

				return msg
			},
		},
		{
			name: "IoT Sensor (Nested Data)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = 3000000000000000001

				// Nested data structure
				_ = msg.Set("sensor.id", "temp-01")
				_ = msg.Set("sensor.location.zone", "warehouse-a")
				_ = msg.Set("sensor.readings.temp_c", 23.5)
				_ = msg.Set("sensor.readings.humidity", 45)
				_ = msg.Set("status", "ok")

				return msg
			},
		},
		{
			name: "Raw Passthrough (Binary Payload)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = 4000000000000000001
				msg.RawPayload = []byte{0xDE, 0xAD, 0xBE, 0xEF} // Dead Beef
				return msg
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.setup()

			// 1. Serialize
			data, err := msgpack.Marshal(original)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			// 2. Output for User Validation
			fmt.Printf("\n=== Scenario: %s ===\n", tt.name)
			fmt.Printf("MsgPack Size: %d bytes\n", len(data))
			fmt.Printf("Hex Dump:\n%s\n", hex.Dump(data))
			fmt.Printf("Single Line Hex: %X\n", data)

			// 3. Deserialize
			var decoded FluxMsg
			if err := msgpack.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			// 4. Validate (Basic Shallow Check)
			if decoded.FluxID != original.FluxID {
				t.Errorf("FluxID mismatch: got %d, want %d", decoded.FluxID, original.FluxID)
			}

			// Check specific fields if they exist in original
			if len(original.Metadata) > 0 {
				if len(decoded.Metadata) != len(original.Metadata) {
					t.Errorf("Metadata len mismatch: got %d, want %d", len(decoded.Metadata), len(original.Metadata))
				}
			}
			if len(original.Data) > 0 {
				// Deep check on one key for sanity
				for k, v := range original.Data {
					// Note: Decoding nested maps results in map[string]interface{} (just like json)
					// We just check existence here for basic validation
					if val, ok := decoded.Get(k); !ok {
						t.Errorf("Missing data key: %s", k)
					} else {
						// Simple type check log
						t.Logf("Key '%s': Original(%T)=%v, Decoded(%T)=%v", k, v, v, val, val)
					}
				}
			}
			if len(original.RawPayload) > 0 {
				if hex.EncodeToString(decoded.RawPayload) != hex.EncodeToString(original.RawPayload) {
					t.Errorf("RawPayload mismatch")
				}
			}
		})
	}
}

func TestFluxMsg_Helpers(t *testing.T) {
	msg := New()

	// 1. Test Set & Get (Simple)
	if err := msg.Set("foo", "bar"); err != nil {
		t.Errorf("Set simple failed: %v", err)
	}
	if v, ok := msg.Get("foo"); !ok || v != "bar" {
		t.Errorf("Get simple failed: got %v, %v", v, ok)
	}

	// 2. Test Set & Get (Nested)
	if err := msg.Set("a.b.c", 42); err != nil {
		t.Errorf("Set nested failed: %v", err)
	}
	if v, ok := msg.Get("a.b.c"); !ok || v != 42 {
		t.Errorf("Get nested failed: got %v, %v", v, ok)
	}

	// 3. Test Set Conflict (Intermediate node is not a map)
	// 'foo' is "bar" (string), trying to set 'foo.baz' should fail
	if err := msg.Set("foo.baz", 100); err == nil {
		t.Error("Expected error when setting child of non-map, got nil")
	}

	// 4. Test GetString
	// Success
	if s, err := msg.GetString("foo"); err != nil || s != "bar" {
		t.Errorf("GetString failed: %v, %s", err, s)
	}
	// Not Found
	if _, err := msg.GetString("missing"); err == nil {
		t.Error("Expected error for missing key, got nil")
	}
	// Type Mismatch
	if _, err := msg.GetString("a.b.c"); err == nil {
		t.Error("Expected error for type mismatch (int is not string), got nil")
	}
}

func TestSystemParsers(t *testing.T) {
	// Test HelloPayload round-trip
	hello := &HelloPayload{
		Name:      "test-rack",
		MachineID: 123,
		IP:        "10.0.0.1",
		Port:      8080,
		Secret:    "secret123",
	}
	helloData, _ := hello.ToData()

	parsedHello, err := ParseHello(helloData)
	if err != nil {
		t.Fatalf("ParseHello failed: %v", err)
	}
	if parsedHello.Name != hello.Name {
		t.Errorf("Name mismatch: got %s", parsedHello.Name)
	}

	// Test HeartbeatPayload round-trip
	hb := &HeartbeatPayload{
		MachineID: 42,
		Stats:     map[string]any{"cpu": 50},
	}
	hbData, _ := hb.ToData()

	parsedHB, err := ParseHeartbeat(hbData)
	if err != nil {
		t.Fatalf("ParseHeartbeat failed: %v", err)
	}
	if parsedHB.MachineID != hb.MachineID {
		t.Errorf("MachineID mismatch: got %d", parsedHB.MachineID)
	}

	// Test HelloResponse round-trip
	helloResp := &HelloResponse{
		Status:   "active",
		Passport: []byte{0x01, 0x02},
		Message:  "Welcome",
	}
	respData, _ := helloResp.ToData()

	parsedResp, err := ParseHelloResponse(respData)
	if err != nil {
		t.Fatalf("ParseHelloResponse failed: %v", err)
	}
	if parsedResp.Status != helloResp.Status {
		t.Errorf("Status mismatch: got %s", parsedResp.Status)
	}

	// Test HeartbeatResponse round-trip
	hbResp := &HeartbeatResponse{
		Status:  "active",
		Command: "sleep",
	}
	hbRespData, _ := hbResp.ToData()

	parsedHBResp, err := ParseHeartbeatResponse(hbRespData)
	if err != nil {
		t.Fatalf("ParseHeartbeatResponse failed: %v", err)
	}
	if parsedHBResp.Command != hbResp.Command {
		t.Errorf("Command mismatch: got %s", parsedHBResp.Command)
	}
}
