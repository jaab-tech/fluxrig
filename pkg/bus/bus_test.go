// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// TestMockBus verifies the memory-based bus implementation used for testing elsewhere.
func TestMockBus(t *testing.T) {
	mb := NewMockBus()

	// 1. Connection (No-op)
	if err := mb.Connect("any-url", ConnectOptions{Name: "mock-client"}); err != nil {
		t.Errorf("Mock connect failed: %v", err)
	}

	// 2. Publish (Storage)
	msg := fluxmsg.New()
	testID1 := uuid.New()
	msg.FluxID = testID1
	if err := mb.Publish(context.Background(), "test.topic", msg); err != nil {
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

	sub, err := mb.Subscribe("test.async", func(ctx context.Context, m *fluxmsg.FluxMsg) {
		received = m
		wg.Done()
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	msg2 := fluxmsg.New()
	testID2 := uuid.New()
	msg2.FluxID = testID2
	_ = mb.Publish(context.Background(), "test.async", msg2)

	// Wait for handler
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if received.FluxID != testID2 {
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

	mb.Close()
}

// TestNatsBus_Disconnected verifies error handling when not connected.
func TestNatsBus_Disconnected(t *testing.T) {
	nb := NewNatsBus("std")

	msg := fluxmsg.New()
	if err := nb.Publish(context.Background(), "foo", msg); err == nil {
		t.Error("Expected error publishing on disconnected bus, got nil")
	}

	if _, err := nb.Subscribe("foo", nil); err == nil {
		t.Error("Expected error subscribing on disconnected bus, got nil")
	}

	nb.Close()
}

// TestNatsBus_Integration tests real Publish/Subscribe flow using ephemeral NATS.
func TestNatsBus_Integration(t *testing.T) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "bus-test",
		StoreDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Failed to start snake: %v", err)
	}
	defer s.Shutdown()

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

	nc, _ := nats.Connect(s.ClientURL())
	defer nc.Close()
	js, _ := jetstream.New(nc)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"flux.>"},
	})
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var received *fluxmsg.FluxMsg

	sub, err := nb.Subscribe("flux.test.bus", func(ctx context.Context, m *fluxmsg.FluxMsg) {
		received = m
		wg.Done()
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	msg := fluxmsg.New()
	testID3 := uuid.New()
	msg.FluxID = testID3
	if err := nb.Publish(context.Background(), "flux.test.bus", msg); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if received == nil {
			t.Error("Received nil message")
		} else if received.FluxID != testID3 {
			t.Errorf("Wrong FluxID: got %v, want %v", received.FluxID, testID3)
		}
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for message")
	}

	rawID := uuid.New()
	if err := nb.PublishRaw(context.Background(), "flux.test.raw", []byte("raw data"), rawID); err != nil {
		t.Fatalf("PublishRaw failed: %v", err)
	}

	nbErr := NewNatsBus("err")
	if err := nbErr.PublishRaw(context.Background(), "any", nil, uuid.Nil); err == nil {
		t.Error("Expected error on PublishRaw with disconnected bus")
	}
}
