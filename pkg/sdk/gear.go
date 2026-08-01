// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// IDGenerator defines the contract for generating unique IDs.
type IDGenerator interface {
	NextFluxID() (uuid.UUID, error)
	NextEntityID(etype idgen.EntityType) uuid.UUID
}

// GearContext provides the initialization context for a Gear.
// It gives access to the hosting Rack's services (Logger, ID Generator, Metrics).
type GearContext interface {
	// Context returns the base context for initialization
	Context() context.Context

	// Config returns the raw configuration map for this gear instance
	Config() map[string]any

	// GearName returns the local name of this gear (e.g. "gateway")
	GearName() string

	// MachineID returns the unique ID of the host Rack
	MachineID() uuid.UUID

	// Logger returns a structured logger scoped to this gear
	Logger() *slog.Logger

	// IDGen returns the Rack's ID generator
	IDGen() IDGenerator

	// Bus returns the message bus interface (for advanced features like KV/Streams)
	Bus() bus.Bus

	// Manager returns the Spec/Scenario Manager
	Manager() manager.Manager

	// ControlPlane returns the global signaling interface
	ControlPlane() any

	// ClusterPublicKey returns the public key of the Mixer that enrolled this Rack
	ClusterPublicKey() []byte

	// Emitter returns this gear's port emitter, for gears that route to named
	// output ports or emit asynchronously (see PortEmitter). Filter gears that
	// only return a single message from Process do not need it.
	Emitter() PortEmitter
}

// PortEmitter emits messages out of a gear's named output ports.
// It is safe to call from Process/ProcessPort and from a gear's own goroutines
// (e.g. a correlation gear's timeout daemon). The runtime recovers panics on
// the emit path, so a bad emission cannot crash the Rack.
type PortEmitter interface {
	// Emit sends msg out of the named port. Port names are hierarchical and
	// dot-separated, with a reserved first segment: "out" (default), "out.<name>"
	// for a specific destination, and "error" for abnormal outcomes.
	// An unknown or unroutable port returns an error rather than misrouting.
	Emit(port string, msg *fluxmsg.FluxMsg) error
}

// NativeGear defines the contract for Go-based components (Internal/Native Gears).
// This interface supports both Passive (Filter) and Active (Source) modes.
type NativeGear interface {
	// Init loads configuration and prepares the gear.
	// It is called once during Rack startup.
	Init(ctx GearContext) error

	// Start begins the active lifecycle of the Gear.
	// It is called exactly once after Init.
	//
	// - For Source Gears (e.g. TCP Listener): Spawn goroutines and use 'emit' to inject messages.
	// - For Filter Gears: You can leave this empty or start background tasks.
	//
	// The context passed here is the "Run Context". If it is canceled, the Gear should stop.
	Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error

	// Process handles an incoming message (Filter/Sink Mode).
	// Returns the transformed message or nil to drop it.
	//
	// - ctx: Request-scoped context
	// - msg: The message to process (Do NOT modify in place if sharing, but here we own it)
	Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error)

	// Drain signals the gear to stop accepting new input but complete pending work.
	// It should block until drained or ctx is canceled.
	Drain(ctx context.Context) error

	// Stop acts as the cleanup hook.
	// Called when the Rack is shutting down or the Scenario is disabled.
	// Should close listener sockets, file handles, etc.
	Stop() error
}

// PortedGear is an optional interface a NativeGear may implement to receive the
// arrival port of each message and route to named output ports.
//
// When a gear implements PortedGear, the runtime delivers each message via
// ProcessPort (carrying the port it arrived on, e.g. "in" vs "in.reply") and
// the gear emits results through GearContext.Emitter() rather than returning
// them. This lets a gear distinguish message roles by arrival port without
// inspecting the payload, and fan results out to several named output ports.
//
// Gears that do not implement PortedGear keep the single-input Process path
// unchanged.
type PortedGear interface {
	NativeGear

	// ProcessPort handles a message that arrived on the named input port.
	// Results are emitted via GearContext.Emitter(); the return is only an
	// error (nil on success). Returning an error is logged and counted, it
	// does not emit anything.
	ProcessPort(ctx context.Context, port string, msg *fluxmsg.FluxMsg) error
}
