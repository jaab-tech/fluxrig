// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

func TestCoatCheck_Certification(t *testing.T) {
	mockBus := bus.NewMockBus()

	// Setup Context
	ctx := &mockGearContext{
		bus: mockBus,
		config: map[string]any{
			"mode":       "store",
			"bucket":     "TEST_BUCKET",
			"key_fields": []string{"meta.id"},
			"on_missing": "forward",
		},
	}

	g := New().(*CoatCheckGear)

	t.Run("Init", func(t *testing.T) {
		err := g.Init(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "store", g.config.Mode)
		assert.Equal(t, "TEST_BUCKET", g.config.Bucket)
	})

	t.Run("Store_Process", func(t *testing.T) {
		msg := fluxmsg.New()
		msg.Metadata["id"] = "123"
		msg.Metadata["data"] = "secret"

		var emitted *fluxmsg.FluxMsg
		err := g.Start(context.Background(), func(m *fluxmsg.FluxMsg) {
			emitted = m
		})
		assert.NoError(t, err)

		out, err := g.Process(context.Background(), msg)
		assert.NoError(t, err)
		assert.Nil(t, out) // Handled by emit
		assert.NotNil(t, emitted)

		// Verify KV contains the coat
		val, _, _ := mockBus.KV().Get("TEST_BUCKET", "MTIz") // "123" base64
		assert.NotNil(t, val)
	})

	t.Run("Restore_Process", func(t *testing.T) {
		// Switch mode to restore
		g.config.Mode = "restore"
		g.restore = &RestoreLogic{gear: g}
		g.store = nil

		msg := fluxmsg.New()
		msg.Metadata["id"] = "123"

		var emitted *fluxmsg.FluxMsg
		g.emit = func(m *fluxmsg.FluxMsg) { emitted = m }

		_, err := g.Process(context.Background(), msg)
		assert.NoError(t, err)
		assert.NotNil(t, emitted)
		assert.Equal(t, "secret", emitted.Metadata["data"])
	})

	t.Run("Missing_Policy", func(t *testing.T) {
		msg := fluxmsg.New()
		msg.Metadata["id"] = "999" // Non-existent

		// Test "forward" (default in our init)
		var emitted *fluxmsg.FluxMsg
		g.emit = func(m *fluxmsg.FluxMsg) { emitted = m }

		_, err := g.Process(context.Background(), msg)
		assert.NoError(t, err)
		assert.NotNil(t, emitted)

		// Test "error"
		g.config.OnMissing = "error"
		_, err = g.Process(context.Background(), msg)
		assert.Error(t, err)

		// Test "drop"
		g.config.OnMissing = "drop"
		g.emit = nil
		out, err := g.Process(context.Background(), msg)
		assert.NoError(t, err)
		assert.Nil(t, out)
	})
}

// ------ Mocks ------

type mockGearContext struct {
	bus    bus.Bus
	config map[string]any
}

func (m *mockGearContext) Context() context.Context { return context.Background() }
func (m *mockGearContext) Config() map[string]any   { return m.config }
func (m *mockGearContext) GearName() string         { return "test" }
func (m *mockGearContext) MachineID() uint64        { return 1 }
func (m *mockGearContext) Logger() *slog.Logger     { return slog.Default() }
func (m *mockGearContext) IDGen() sdk.IDGenerator   { return nil }
func (m *mockGearContext) Bus() bus.Bus             { return m.bus }
func (m *mockGearContext) Manager() manager.Manager { return nil }
func (m *mockGearContext) ControlPlane() any        { return nil }
