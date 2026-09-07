// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// ModeImpl defines the interface for server/client implementations.
type ModeImpl interface {
	Start(ctx context.Context) error
	Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error)
	Drain(ctx context.Context) error
	Stop() error
}

// Gear implements sdk.NativeGear for Simple TCP
type Gear struct {
	config *Config
	log    *slog.Logger
	ctx    sdk.GearContext
	emit   func(*fluxmsg.FluxMsg)

	impl ModeImpl
}

// Ensure interface compliance
var _ sdk.NativeGear = (*Gear)(nil)

// Init loads configuration and prepares the gear.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	g.log = ctx.Logger()

	cfg, err := ParseConfig(ctx.Config())
	if err != nil {
		return fmt.Errorf("io_tcp: invalid config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("io_tcp: validation failed: %w", err)
	}
	g.config = cfg

	g.log.Info("initialized", "mode", cfg.Mode, "bind", cfg.Bind, "connect", cfg.Connect)
	return nil
}

// Start begins the active lifecycle (Listener/Dialer).
func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.log.Info("starting gear")
	g.emit = emit

	// Validate
	if g.config.Mode == "" {
		return fmt.Errorf("io_tcp: missing mode")
	}

	switch g.config.Mode {
	case "server":
		if g.config.Bind == "" {
			return fmt.Errorf("io_tcp: missing bind address")
		}
		g.impl = NewServer(g.config, g.ctx.Logger(), g.emit, g.ctx.IDGen())
	case "client":
		if g.config.Connect == "" {
			return fmt.Errorf("io_tcp: missing connect address")
		}
		g.impl = NewClient(g.config, g.ctx.Logger(), g.emit, g.ctx.IDGen())
	default:
		return fmt.Errorf("io_tcp: unknown mode %q", g.config.Mode)
	}

	return g.impl.Start(ctx)
}

// Process handles egress messages (writing to TCP).
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	if g.impl == nil {
		return nil, fmt.Errorf("io_tcp: not initialized")
	}
	return g.impl.Process(ctx, msg)
}

// Stop closes the gear resources. (Renamed to Close in diff, but keeping original name for interface compliance)
func (g *Gear) Stop() error {
	g.log.Info("stopping gear")
	if g.impl != nil {
		// For io_tcp, we can reuse Stop() or just close listener.
		// Ideally we would implement proper Drain in io_tcp server too.
		return g.impl.Stop()
	}
	return nil
}

// Drain signals the gear to stop accepting new input and to let what is in
// flight finish. It returns nil when there is nothing left to wait for.
func (g *Gear) Drain(ctx context.Context) error {
	if g.impl == nil {
		return nil
	}
	return g.impl.Drain(ctx)
}
