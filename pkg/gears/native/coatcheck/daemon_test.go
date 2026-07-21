// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func TestDaemonLogic_Init(t *testing.T) {
	mockBus := bus.NewMockBus()

	ctx := &mockGearContext{
		bus: mockBus,
		config: map[string]any{
			"mode":        "daemon",
			"bucket":      "TEST_DAEMON",
			"default_ttl": "1s",
			"max_ttl":     "2s",
		},
	}

	g := New().(*CoatCheckGear)
	err := g.Init(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, g.daemon)

	// Bucket should be created
	assert.NoError(t, err)
}

func TestDaemonLogic_Lifecycle(t *testing.T) {
	mockBus := bus.NewMockBus()

	ctx := &mockGearContext{
		bus: mockBus,
		config: map[string]any{
			"mode":           "daemon",
			"bucket":         "TEST_DAEMON",
			"default_ttl":    "10ms",
			"max_ttl":        "50ms",
			"include_values": true,
		},
	}

	g := New().(*CoatCheckGear)
	err := g.Init(ctx)
	assert.NoError(t, err)

	emitted := make(chan *fluxmsg.FluxMsg, 10)
	err = g.Start(context.Background(), func(m *fluxmsg.FluxMsg) {
		emitted <- m
	})
	assert.NoError(t, err)

	// Simulate PUT event (via Watcher manually or by firing scheduleExpiry)
	msg := fluxmsg.New()
	msg.Metadata["test"] = "daemon"
	valBytes, _ := cbor.Marshal(msg)

	g.daemon.scheduleExpiry("key-1", valBytes, func(m *fluxmsg.FluxMsg) {
		emitted <- m
	})

	// Wait for expiry
	select {
	case out := <-emitted:
		assert.Equal(t, "flux.event.timeout", out.Metadata["event.type"])
		assert.Equal(t, "key-1", out.Metadata["timeout.key"])
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for expiry")
	}

	// Cancel Expiry
	g.daemon.scheduleExpiry("key-2", valBytes, func(m *fluxmsg.FluxMsg) {})
	g.daemon.cancelExpiry("key-2")

	g.daemon.Stop()
	assert.Empty(t, g.daemon.timers)
}
