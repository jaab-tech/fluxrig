// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	// Initialize limits for tests
	SetLimits(64, 2*1024*1024)
	m.Run()
}

func TestFluxMsg_Scenarios(t *testing.T) {
	tests := []struct {
		name  string
		setup func() *FluxMsg
	}{
		{
			name: "Minimal (Identity Only)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = uuid.New()
				msg.TsInit = time.Now().UnixNano()
				return msg
			},
		},
		{
			name: "Standard Payment (ISO8583-like)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = uuid.New()
				msg.TraceID = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" // W3C format

				msg.Metadata["iso.mti"] = "0200"
				msg.Metadata["peer.ip"] = "10.0.0.5"

				_ = msg.Set("pan_masked", "4111xxxxxxxx1111")
				_ = msg.Set("amount", 1500.50)
				_ = msg.Set("currency", 840)

				return msg
			},
		},
		{
			name: "IoT Sensor (Nested Data)",
			setup: func() *FluxMsg {
				msg := New()
				msg.FluxID = uuid.New()

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
				msg.FluxID = uuid.New()
				msg.RawPayload = []byte{0xDE, 0xAD, 0xBE, 0xEF}
				return msg
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.setup()

			data, err := cbor.Marshal(original)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			fmt.Printf("\n=== Scenario: %s ===\n", tt.name)
			fmt.Printf("CBOR Size: %d bytes\n", len(data))
			fmt.Printf("Hex Dump:\n%s\n", hex.Dump(data))

			var decoded FluxMsg
			if err := cbor.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if decoded.FluxID != original.FluxID {
				t.Errorf("FluxID mismatch: got %v, want %v", decoded.FluxID, original.FluxID)
			}
		})
	}
}

func TestFluxMsg_Helpers(t *testing.T) {
	msg := New()

	_ = msg.Set("foo", "bar")
	if v, ok := msg.Get("foo"); !ok || v != "bar" {
		t.Errorf("Get simple failed: got %v, %v", v, ok)
	}

	_ = msg.Set("a.b.c", 42)
	if v, ok := msg.Get("a.b.c"); !ok || v != 42 {
		t.Errorf("Get nested failed: got %v, %v", v, ok)
	}
}

func TestSystemParsers(t *testing.T) {
	machineID := uuid.New()

	hello := &HelloPayload{
		Name:      "test-rack",
		MachineID: machineID,
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

	hb := &HeartbeatPayload{
		MachineID: machineID,
		Stats:     map[string]any{"cpu": 50},
	}
	hbData, _ := hb.ToData()

	parsedHB, err := ParseHeartbeat(hbData)
	if err != nil {
		t.Fatalf("ParseHeartbeat failed: %v", err)
	}
	if parsedHB.MachineID != hb.MachineID {
		t.Errorf("MachineID mismatch: got %v", parsedHB.MachineID)
	}
}

func TestFluxMsg_ValidationLimits(t *testing.T) {
	t.Run("Exceed MaxHops", func(t *testing.T) {
		msg := New()
		for i := 0; i <= MaxHops; i++ {
			msg.Path = append(msg.Path, &Hop{GearID: uuid.New()})
		}
		if err := msg.Validate(); err == nil {
			t.Error("expected error for exceeding MaxHops, got nil")
		}
	})

	t.Run("Exceed MaxPayloadSize", func(t *testing.T) {
		msg := New()
		msg.RawPayload = make([]byte, MaxPayloadSize+1)
		if err := msg.Validate(); err == nil {
			t.Error("expected error for exceeding MaxPayloadSize, got nil")
		}
	})

	t.Run("Valid Limits", func(t *testing.T) {
		msg := New()
		msg.Path = append(msg.Path, &Hop{GearID: uuid.New()})
		msg.RawPayload = []byte("small")
		if err := msg.Validate(); err != nil {
			t.Errorf("unexpected error for valid message: %v", err)
		}
	})
}
