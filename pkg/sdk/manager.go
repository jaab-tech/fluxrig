// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// DefaultContext implements GearContext
type DefaultContext struct {
	baseCtx   context.Context // Renamed to avoid collision with interface method
	config    map[string]any
	gearName  string
	machineID uint64
	logger    *slog.Logger
	idGen     IDGenerator
	bus       bus.Bus
	mgr       manager.Manager
}

// NewDefaultContext creates a partial GearContext with the Manager pre-set.
// The remaining fields (baseCtx, config, gearName, machineID, logger, idGen, bus)
// are populated by the runtime when the gear is initialized.
func NewDefaultContext(mgr manager.Manager) *DefaultContext {
	return &DefaultContext{mgr: mgr}
}

func (c *DefaultContext) Context() context.Context {
	return c.baseCtx
}

func (c *DefaultContext) Config() map[string]any {
	return c.config
}

func (c *DefaultContext) GearName() string {
	return c.gearName
}

func (c *DefaultContext) MachineID() uint64 {
	return c.machineID
}

func (c *DefaultContext) Logger() *slog.Logger {
	return c.logger
}

func (c *DefaultContext) IDGen() IDGenerator {
	return c.idGen
}

func (c *DefaultContext) Bus() bus.Bus {
	return c.bus
}

func (c *DefaultContext) Manager() manager.Manager {
	return c.mgr
}
