// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func setupBus(t *testing.T) (*snake.Server, *bus.NatsBus, string) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "run-test",
		StoreDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Failed to start snake: %v", err)
	}

	b := bus.NewNatsBus("flux")
	url := s.ClientURL()
	if errCon := b.Connect(url, bus.ConnectOptions{
		Name:           "test-runner",
		ConnectTimeout: 2 * time.Second,
		ReconnectWait:  1 * time.Second,
	}); errCon != nil {
		t.Fatalf("Bus connect failed: %v", errCon)
	}

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("NATS connect failed: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream init failed: %v", err)
	}
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"flux.>"},
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

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("NATS connect failed: %v", err)
	}
	defer nc.Close()

	sub, err := nc.SubscribeSync("flux.agent.hello")
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()

	machineID := uuid.New()
	payload := &fluxmsg.HelloPayload{
		Name:      "unit-test-rack",
		MachineID: machineID,
		IP:        "127.0.0.1",
		Port:      9999,
		Secret:    "secret-123",
	}

	gen, _ := idgen.New(uuid.New())
	if errSend := sendHello(b, payload, gen); errSend != nil {
		t.Fatalf("sendHello failed: %v", errSend)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("Did not receive hello: %v", err)
	}

	var fMsg fluxmsg.FluxMsg
	if errUnmarshal := cbor.Unmarshal(msg.Data, &fMsg); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	if fMsg.SrcGearID != machineID {
		t.Errorf("Expected SrcID %v, got %v", machineID, fMsg.SrcGearID)
	}

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

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("NATS connect failed: %v", err)
	}
	defer nc.Close()
	sub, _ := nc.SubscribeSync("flux.agent.heartbeat")
	_ = nc.Flush()

	machineID := uuid.New()
	gen, _ := idgen.New(uuid.New())
	hbCfg := &config.RackConfig{}
	if errHB := sendHeartbeat(context.Background(), b, machineID, hbCfg, gen); errHB != nil {
		t.Fatal(errHB)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatal("No heartbeat received")
	}

	var fMsg fluxmsg.FluxMsg
	if errUnmarshal := cbor.Unmarshal(msg.Data, &fMsg); errUnmarshal != nil {
		t.Fatalf("Failed to unmarshal FluxMsg: %v", errUnmarshal)
	}

	if fMsg.SrcGearID != machineID {
		t.Error("Wrong SrcID")
	}

	hb, err := fluxmsg.ParseHeartbeat(fMsg.Data)
	if err != nil {
		t.Fatalf("Failed to parse heartbeat: %v", err)
	}

	if hb.MachineID != machineID {
		t.Error("Wrong HB ID")
	}

	if _, ok := hb.Stats["goroutines"]; !ok {
		t.Error("Missing goroutines stat")
	}
}
