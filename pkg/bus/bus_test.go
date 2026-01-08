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

package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestMockBus verifies the memory-based bus implementation used for testing elsewhere.
func TestMockBus(t *testing.T) {
	mb := NewMockBus()
	// Note: Close is a no-op but we call it explicitly to ensure coverage

	// 1. Connection (No-op)
	if err := mb.Connect("any-url", ConnectOptions{Name: "mock-client"}); err != nil {
		t.Errorf("Mock connect failed: %v", err)
	}

	// 2. Publish (Storage)
	msg := fluxmsg.New()
	msg.FluxID = 123
	if err := mb.Publish("test.topic", msg); err != nil {
		t.Errorf("Mock publish failed: %v", err)
	}

	// Check storage
	if len(mb.PublishedMessages["test.topic"]) != 1 {
		t.Errorf("Message not stored in mock")
	}

	// 3. Subscribe & Handler Loopback
	var wg sync.WaitGroup
	wg.Add(1)
	var received *fluxmsg.FluxMsg

	sub, err := mb.Subscribe("test.async", func(m *fluxmsg.FluxMsg) {
		received = m
		wg.Done()
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	msg2 := fluxmsg.New()
	msg2.FluxID = 456
	_ = mb.Publish("test.async", msg2)

	// Wait for handler
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if received.FluxID != 456 {
			t.Errorf("Handler received wrong message ID")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for mock handler")
	}

	// 4. Unsubscribe
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("Unsubscribe failed: %v", err)
	}

	// 5. GetMessages
	messages := mb.GetMessages("test.topic")
	if len(messages) != 1 {
		t.Errorf("GetMessages returned wrong count: expected 1, got %d", len(messages))
	}

	// GetMessages for non-existent topic
	noMessages := mb.GetMessages("no.topic")
	if len(noMessages) != 0 {
		t.Errorf("Expected empty slice for non-existent topic")
	}

	// Explicitly call Close for coverage
	mb.Close()
}

// TestNatsBus_Disconnected verifies error handling when not connected.
func TestNatsBus_Disconnected(t *testing.T) {
	nb := NewNatsBus("std")
	// Do not Connect()

	msg := fluxmsg.New()
	if err := nb.Publish("foo", msg); err == nil {
		t.Error("Expected error publishing on disconnected bus, got nil")
	}

	if _, err := nb.Subscribe("foo", nil); err == nil {
		t.Error("Expected error subscribing on disconnected bus, got nil")
	}

	// Close should be safe even if nil
	nb.Close()
}

// TestNatsBus_Integration tests real Publish/Subscribe flow using ephemeral NATS.
func TestNatsBus_Integration(t *testing.T) {
	// 1. Start Snake (ephemeral NATS)
	s, err := snake.NewServer(snake.Config{
		Port:        -1,
		ClusterName: "bus-test",
		StoreDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Failed to start snake: %v", err)
	}
	defer s.Shutdown()

	// 1. Setup
	nb := NewNatsBus("flux")
	require.NotNil(t, nb)
	if errConn := nb.Connect(s.ClientURL(), ConnectOptions{
		Name:           "test-client",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  1 * time.Second,
	}); errConn != nil {
		t.Fatalf("Connect failed: %v", errConn)
	}
	defer nb.Close()

	// 3. Configure JetStream (Create stream)
	nc, _ := nats.Connect(s.ClientURL())
	defer nc.Close()
	js, _ := jetstream.New(nc)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"fluxrig.>"},
	})
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}

	// 4. Subscribe
	var wg sync.WaitGroup
	wg.Add(1)
	var received *fluxmsg.FluxMsg

	sub, err := nb.Subscribe("fluxrig.test.bus", func(m *fluxmsg.FluxMsg) {
		received = m
		wg.Done()
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// 5. Publish
	msg := fluxmsg.New()
	msg.FluxID = 789
	if err := nb.Publish("fluxrig.test.bus", msg); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// 6. Wait for handler
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if received == nil {
			t.Error("Received nil message")
		} else if received.FluxID != 789 {
			t.Errorf("Wrong FluxID: got %d", received.FluxID)
		}
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for message")
	}
}
