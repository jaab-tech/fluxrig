// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/nats-io/nats.go"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func TestRouter_Lifecycle(t *testing.T) {
	// 1. Start Ephemeral NATS (Snake)
	tmpDir := t.TempDir()
	snk, err := snake.NewServer(snake.Config{
		Port:        -1, // Random port
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	})
	if err != nil {
		t.Fatalf("Failed to start snake: %v", err)
	}
	defer snk.Shutdown()

	logger := watermill.NewStdLogger(false, false)

	// 2. Create Router Wrapper
	r, err := NewRouter(logger)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// 3. Configure JetStream
	err = r.ConfigureJetStream(snk.ClientURL(), "test-cluster", false, "", "", "", logger)
	if err != nil {
		t.Fatalf("Failed to configure JS: %v", err)
	}

	if r.Pub == nil || r.Sub == nil {
		t.Fatal("Pub or Sub is nil")
	}

	// 4. Test Pub/Sub
	topic := "fluxrig.test.topic"
	done := make(chan struct{})

	// Subscribe
	msgs, err := r.Sub.Subscribe(context.Background(), topic)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	go func() {
		select {
		case msg := <-msgs:
			if string(msg.Payload) == "hello-flux" {
				msg.Ack()
				close(done)
			}
		case <-time.After(5 * time.Second):
			// timeout
		}
	}()

	// Publish
	err = r.Pub.Publish(topic, message.NewMessage(watermill.NewUUID(), []byte("hello-flux")))
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait
	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for message")
	}

	// 5. Close
	if err := r.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestRawNATSMarshaler(t *testing.T) {
	m := RawNATSMarshaler{}

	// Marshal
	wmMsg := message.NewMessage("uuid-1", []byte("payload"))
	natsMsg, err := m.Marshal("topic.a", wmMsg)
	if err != nil {
		t.Fatal(err)
	}

	if natsMsg.Subject != "topic.a" {
		t.Errorf("Subject mismatch: %s", natsMsg.Subject)
	}
	if string(natsMsg.Data) != "payload" {
		t.Errorf("Payload mismatch: %s", string(natsMsg.Data))
	}

	// Unmarshal
	nMsg := &nats.Msg{
		Subject: "topic.b",
		Data:    []byte("response"),
	}

	wmMsg2, err := m.Unmarshal(nMsg)
	if err != nil {
		t.Fatal(err)
	}

	if string(wmMsg2.Payload) != "response" {
		t.Error("Unmarshaled payload mismatch")
	}
}

func TestRouter_CloseEmpty(t *testing.T) {
	logger := watermill.NewStdLogger(false, false)
	r, err := NewRouter(logger)
	if err != nil {
		t.Fatal(err)
	}
	// Pub/Sub are nil
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRouter_Run(t *testing.T) {
	logger := watermill.NewStdLogger(false, false)
	r, err := NewRouter(logger)
	if err != nil {
		t.Fatal(err)
	}

	// Run in goroutine
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)

	go func() {
		done <- r.Run(ctx)
	}()

	// Give router time to start, then close it
	time.Sleep(50 * time.Millisecond)

	// Close router (this triggers clean shutdown)
	_ = r.Router.Close()
	cancel()

	// Wait for result (should complete quickly after close)
	select {
	case <-done:
		// Success - Run exited
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Run to complete")
	}
}
