package commands

import (
	"context"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/vmihailenco/msgpack/v5"
)

func setupBus(t *testing.T) (*snake.Server, *bus.NatsBus, string) {
	// 1. Start Snake
	s, err := snake.NewServer(snake.Config{
		Port:        -1,
		ClusterName: "run-test",
	})
	if err != nil {
		t.Fatalf("Failed to start snake: %v", err)
	}

	// 2. Connect Bus
	// We need to verify that NewNatsBus is called
	// Since we can't easily mock the bus package function without dependency injection,
	// we will rely on the interface in the real code.
	// For this test, we just check the config parsing.
	b := bus.NewNatsBus("flux")
	url := s.ClientURL()
	if err := b.Connect(url, "test-runner", 2*time.Second, 1*time.Second); err != nil {
		s.Shutdown()
		t.Fatalf("Failed to connect bus: %v", err)
	}

	// 3. Create Stream (Required for JS Publish)
	nc, _ := nats.Connect(url)
	defer nc.Close()
	js, _ := jetstream.New(nc)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"fluxrig.>"},
	})
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}

	return s, b, url
}

func TestSendHello(t *testing.T) {
	s, b, url := setupBus(t)
	defer s.Shutdown()
	defer b.Close()

	// 1. Subscribe to verify
	nc, _ := nats.Connect(url)
	defer nc.Close()

	sub, err := nc.SubscribeSync("fluxrig.agent.hello")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Send Hello
	payload := &fluxmsg.HelloPayload{
		Name:      "unit-test-rack",
		MachineID: 123,
		IP:        "127.0.0.1",
		Port:      9999,
		Secret:    "secret-123",
	}

	if err := sendHello(b, payload); err != nil {
		t.Fatalf("sendHello failed: %v", err)
	}

	// 3. Verify
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("Did not receive hello: %v", err)
	}

	// Unpack FluxMsg
	var fMsg fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(msg.Data, &fMsg); err != nil {
		t.Fatal(err)
	}

	if fMsg.SrcGearID != 123 {
		t.Errorf("Expected SrcID 123, got %d", fMsg.SrcGearID)
	}

	// Unpack Payload using Helper (since Data is map[string]any)
	p, err := fluxmsg.ParseHello(fMsg.Data)
	if err != nil {
		t.Fatalf("Failed to parse hello payload: %v", err)
	}

	if p.Name != "unit-test-rack" {
		t.Errorf("Name mismatch: %s", p.Name)
	}
}

func TestSendHeartbeat(t *testing.T) {
	s, b, url := setupBus(t)
	defer s.Shutdown()
	defer b.Close()

	nc, _ := nats.Connect(url)
	defer nc.Close()
	sub, _ := nc.SubscribeSync("fluxrig.agent.heartbeat")
	nc.Flush() // Ensure subscription is active before sending

	if err := sendHeartbeat(b, 456); err != nil {
		t.Fatal(err)
	}

	msg, err := sub.NextMsg(5 * time.Second) // Increased timeout for CI
	if err != nil {
		t.Fatal("No heartbeat received")
	}

	var fMsg fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(msg.Data, &fMsg); err != nil {
		t.Fatalf("Failed to unmarshal FluxMsg: %v", err)
	}

	if fMsg.SrcGearID != 456 {
		t.Error("Wrong SrcID")
	}

	hb, err := fluxmsg.ParseHeartbeat(fMsg.Data)
	if err != nil {
		t.Fatalf("Failed to parse heartbeat: %v", err)
	}

	if hb.MachineID != 456 {
		t.Error("Wrong HB ID")
	}

	// Check stats presence (goroutines)
	if _, ok := hb.Stats["goroutines"]; !ok {
		t.Error("Missing goroutines stat")
	}
}

// TestConfigLoading can invoke internal logic if exposed, but config loading is mostly
// `pkg/config`. We want to test logic in `RunAgent` that handles defaults.
// This is harder without refactoring RunAgent to be non-blocking or mockable.
// Skipping RunAgent loop for now, helper coverage covers protocol.
