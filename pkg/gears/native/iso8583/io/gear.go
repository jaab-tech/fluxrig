// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Gear implements sdk.NativeGear for ISO8583 I/O with length-prefixed framing.
type Gear struct {
	config *Config
	log    *slog.Logger
	ctx    sdk.GearContext

	// Mode implementations
	server *Server
	client *Client
}

// Ensure interface compliance
var _ sdk.NativeGear = (*Gear)(nil)

// Init loads configuration and prepares the gear.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	g.log = ctx.Logger()

	cfg, err := ParseConfig(ctx.Config())
	if err != nil {
		return fmt.Errorf("iso8583: invalid config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("iso8583: validation failed: %w", err)
	}
	g.config = cfg

	g.log.Info("initialized",
		"mode", cfg.Mode,
		"variant", cfg.Variant,
		"bind", cfg.Bind,
		"connect", cfg.Connect,
		"frame_length_size", cfg.FrameLengthSize,
		"frame_length_endian", cfg.FrameLengthEndian,
		"protocol_header_size", cfg.ProtocolHeaderSize,
		"encoding", cfg.Encoding,
		"tpdu", cfg.TPDUEnabled,
		"heuristic", cfg.HeuristicValidation,
	)
	return nil
}

// Start begins the active lifecycle (Listener/Dialer).
func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.log.Info("starting gear")

	switch g.config.Mode {
	case ModeServer:
		g.server = NewServer(g.config, g.log, emit, g.ctx.IDGen())
		if err := g.server.Start(ctx); err != nil {
			return err
		}
	case ModeClient:
		g.client = NewClient(g.config, g.log, emit, g.ctx.IDGen())
		if err := g.client.Start(ctx); err != nil {
			return err
		}
	}

	// --- Control Plane Integration (ADR 0020) ---
	if cp, ok := g.ctx.ControlPlane().(ctrl.ControlPlane); ok {
		cmdChan, err := cp.Subscribe(g.ctx.GearName())
		if err != nil {
			g.log.Error("failed to subscribe to control plane", "error", err)
		} else {
			go g.handleControlPlane(cmdChan)
		}
	}

	return nil
}

func (g *Gear) handleControlPlane(cmds <-chan ctrl.Command) {
	for cmd := range cmds {
		switch cmd.Cmd {
		case "conn.close":
			connID := cmd.Args["conn_id"]
			g.log.Warn("Kill Switch triggered via Control Plane", "conn_id", connID, "src", cmd.Src)
			var err error
			if g.server != nil {
				err = g.server.Disconnect(connID)
			} else if g.client != nil {
				err = g.client.Disconnect(connID)
			}
			if err != nil {
				g.log.Error("Disconnect failed", "conn_id", connID, "error", err)
			}
		default:
			g.log.Debug("received unknown control command", "cmd", cmd.Cmd)
		}
	}
}

// Process handles egress messages (writing to TCP).
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	if g.server != nil {
		return g.server.Process(ctx, msg)
	}
	if g.client != nil {
		return g.client.Process(ctx, msg)
	}
	return nil, nil
}

// Stop closes resources.
func (g *Gear) Stop() error {
	g.log.Info("stopping gear")
	var err error
	if g.server != nil {
		if e := g.server.Stop(); e != nil {
			err = e
		}
	}
	if g.client != nil {
		if e := g.client.Stop(); e != nil {
			err = e
		}
	}
	return err
}

// Drain signals the gear to stop accepting new input but complete pending work.
func (g *Gear) Drain(ctx context.Context) error {
	g.log.Info("draining gear")
	var err error
	if g.server != nil {
		if e := g.server.Drain(ctx); e != nil {
			err = e
		}
	}
	if g.client != nil {
		if e := g.client.Drain(ctx); e != nil {
			err = e
		}
	}
	return err
}
