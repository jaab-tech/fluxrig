// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warpstreamlabs/bento/public/service"
)

func TestConfigToYaml(t *testing.T) {
	m := map[string]any{
		"input": map[string]any{
			"generate": map[string]any{
				"count":   10,
				"mapping": `root = "hello world"`,
			},
		},
	}
	bytes, err := MapToYaml(m)
	require.NoError(t, err)
	assert.Contains(t, string(bytes), "generate")
	assert.Contains(t, string(bytes), "hello world")
}

func TestBridge(t *testing.T) {
	// 1. Flux -> Bento
	fm := fluxmsg.New()
	fm.FluxID = 123456789
	fm.TraceID = "abc-123"
	fm.Data["foo"] = "bar"
	fm.Metadata["user"] = "admin"

	bm := ToBentoMessage(fm)

	// Verify Bento Content
	data, err := bm.AsStructured()
	require.NoError(t, err)
	asMap := data.(map[string]any)
	assert.Equal(t, "bar", asMap["foo"])

	// Verify Bento Meta
	v, _ := bm.MetaGet("user")
	assert.Equal(t, "admin", v)
	v, _ = bm.MetaGet("trace_id")
	assert.Equal(t, "abc-123", v)
	v, _ = bm.MetaGet("flux_id")
	assert.Equal(t, "123456789", v)

	// 2. Bento -> Flux
	outFm, err := FromBentoMessage(bm)
	require.NoError(t, err)
	assert.Equal(t, "bar", outFm.Data["foo"])
	assert.Equal(t, "admin", outFm.Metadata["user"])
	assert.Equal(t, "abc-123", outFm.TraceID)
}

// MockContext implements sdk.GearContext for testing
type MockContext struct {
	config map[string]any
}

func (m *MockContext) Context() context.Context { return context.Background() }
func (m *MockContext) Config() map[string]any   { return m.config }
func (m *MockContext) GearName() string         { return "test-gear" }
func (m *MockContext) MachineID() uint64        { return 1 }
func (m *MockContext) Logger() *slog.Logger     { return slog.Default() }
func (m *MockContext) IDGen() sdk.IDGenerator   { return nil }
func (m *MockContext) Bus() bus.Bus             { return nil }
func (m *MockContext) Manager() manager.Manager { return nil }
func (m *MockContext) ControlPlane() any        { return nil }

func TestStrictValidation(t *testing.T) {
	g := New()
	// Invalid Config (Bloblang Syntax Error)
	ctx := &MockContext{
		config: map[string]any{
			"bento": map[string]any{
				"pipeline": map[string]any{
					"processors": []any{
						map[string]any{"mapping": "root = this.broken((("},
					},
				},
			},
		},
	}

	err := g.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse bento yaml")
}

func TestAutoWiring(t *testing.T) {
	g := New()
	ctx := &MockContext{
		config: map[string]any{
			"ports": map[string]any{
				"inputs":  []any{"in"},
				"outputs": []any{"out"},
			},
			"bento": map[string]any{
				"pipeline": map[string]any{
					"processors": []any{
						map[string]any{"mapping": "root = this"},
					},
				},
			},
		},
	}

	err := g.Init(ctx)
	require.NoError(t, err)

	// Check Config Injection
	// Structure: input -> flux_in_<name>
	inBlock, hasInBlock := g.config.Bento["input"].(map[string]any)
	assert.True(t, hasInBlock, "Should have input block")
	if hasInBlock {
		_, hasPlugin := inBlock[g.inputType]
		assert.True(t, hasPlugin, "Should have specific input plugin")
	}

	outBlock, hasOutBlock := g.config.Bento["output"].(map[string]any)
	assert.True(t, hasOutBlock, "Should have output block")
	if hasOutBlock {
		_, hasPlugin := outBlock[g.outputType]
		assert.True(t, hasPlugin, "Should have specific output plugin")
	}
}

func TestEndToEndFlow(t *testing.T) {
	g := New()
	ctx := &MockContext{
		config: map[string]any{
			"ports": map[string]any{"inputs": []any{"in"}, "outputs": []any{"out"}},
			"bento": map[string]any{
				"pipeline": map[string]any{
					"processors": []any{
						map[string]any{"mapping": "root = this\nroot.processed = true"},
					},
				},
			},
		},
	}
	require.NoError(t, g.Init(ctx))

	outChan := make(chan *fluxmsg.FluxMsg, 10)
	emit := func(m *fluxmsg.FluxMsg) {
		outChan <- m
	}

	// Start
	runtimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, g.Start(runtimeCtx, emit))
	defer func() { _ = g.Stop() }()

	// Process (Simulate SDK calling Process)
	inMsg := fluxmsg.New()
	inMsg.Data["foo"] = "bar"

	// Process returns nil, nil because it pushes to Bento async
	res, err := g.Process(runtimeCtx, inMsg)
	assert.Nil(t, res)
	assert.NoError(t, err)

	// Wait for Output (bridge is async)
	select {
	case outMsg := <-outChan:
		assert.Equal(t, "bar", outMsg.Data["foo"])
		assert.Equal(t, true, outMsg.Data["processed"])
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestMapToYaml_Nil(t *testing.T) {
	_, err := MapToYaml(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "map is nil")
}

func TestBridge_NonMap(t *testing.T) {
	// Create a message with a string payload (not map[string]any)
	m := service.NewMessage([]byte("hello"))
	m.SetStructured("hello world") // Root is string

	fm, err := FromBentoMessage(m)
	require.NoError(t, err)
	// Should fall back to RawPayload or handle scalar logic
	// Logic says: if map -> Data. Else -> RawPayload (via AsBytes)
	assert.Empty(t, fm.Data)
	assert.Contains(t, string(fm.RawPayload), "hello world")
}

func TestProcess_Cancel(t *testing.T) {
	g := New()
	// No start needed, just channel logic
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Fill channel to force block
	for i := 0; i < 100; i++ {
		g.inChan <- fluxmsg.New()
	}

	msg := fluxmsg.New()
	_, err := g.Process(ctx, msg)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRead_Cancel(t *testing.T) {
	g := New()
	input := &fluxInput{g: g}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg, ack, err := input.Read(ctx)
	assert.Nil(t, msg)
	assert.Nil(t, ack)
	assert.ErrorIs(t, err, service.ErrEndOfInput)
}

func TestStart_InvalidConfig(t *testing.T) {
	g := New()
	// 1. Init with VALID config to setup Env
	validCtx := &MockContext{
		config: map[string]any{
			"bento": map[string]any{
				"pipeline": map[string]any{
					"processors": []any{
						map[string]any{"mapping": "root = this"},
					},
				},
			},
		},
	}
	require.NoError(t, g.Init(validCtx))

	// 2. Corrupt config internally (simulating runtime modification or bad state)
	g.config.Bento = map[string]any{
		"pipeline": map[string]any{
			"processors": "invalid_should_be_list",
		},
	}
	// We need a context
	ctx := context.Background()
	emit := func(m *fluxmsg.FluxMsg) {}

	err := g.Start(ctx, emit)
	// Start attempts to marshal config to recreate stream builder
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bento config invalid in start")
}
