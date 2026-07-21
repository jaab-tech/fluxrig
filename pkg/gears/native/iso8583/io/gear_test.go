// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// MockGearContext implements sdk.GearContext for testing
type MockGearContext struct {
	config map[string]any
	logger *slog.Logger
	idGen  *idgen.IDGenerator
}

func (m *MockGearContext) Context() context.Context { return context.Background() }
func (m *MockGearContext) Config() map[string]any   { return m.config }
func (m *MockGearContext) GearName() string         { return "test-iso-gear" }
func (m *MockGearContext) MachineID() uuid.UUID     { return uuid.Nil }
func (m *MockGearContext) Logger() *slog.Logger     { return m.logger }
func (m *MockGearContext) IDGen() sdk.IDGenerator   { return m.idGen }
func (m *MockGearContext) Bus() bus.Bus             { return nil }
func (m *MockGearContext) Manager() manager.Manager { return nil }
func (m *MockGearContext) ControlPlane() any        { return nil }
func (m *MockGearContext) ClusterPublicKey() []byte { return nil }

func TestGear_Lifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	gen, _ := idgen.New(uuid.New())

	ctx := &MockGearContext{
		config: map[string]any{
			"mode":    "server",
			"bind":    ":0", // Random port
			"variant": "mastercard",
		},
		logger: logger,
		idGen:  gen,
	}

	gear := &Gear{}

	// 1. Init
	if err := gear.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// 2. Start
	startCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- gear.Start(startCtx, func(msg *fluxmsg.FluxMsg) {})
	}()

	// Give it a moment to start the listener
	time.Sleep(100 * time.Millisecond)

	// 3. Stop
	if err := gear.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}
