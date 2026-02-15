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

package sdk

import (
	"context"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// IDGenerator defines the contract for generating unique IDs.
type IDGenerator interface {
	NextFluxID() (uint64, error)
	NextEntityID(etype idgen.EntityType) uint64
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
	MachineID() uint64

	// Logger returns a structured logger scoped to this gear
	Logger() *slog.Logger

	// IDGen returns the Rack's ID generator
	IDGen() IDGenerator

	// Bus returns the message bus interface (for advanced features like KV/Streams)
	Bus() bus.Bus

	// Manager returns the Spec/Scenario Manager
	Manager() manager.Manager
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
	// The context passed here is the "Run Context". If it is cancelled, the Gear should stop.
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
