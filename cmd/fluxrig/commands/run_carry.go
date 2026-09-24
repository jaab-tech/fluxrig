// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
)

// ErrRejoin ends a session that started without the bus once the bus answers, so that
// the next session, which has it, takes over the gears that are already running.
var ErrRejoin = errors.New("rejoin: the bus is back")

// sessionCarry is what a session that started without the bus hands to the next
// one: the running runtime, with its gears and the connections they hold, and what
// goes with it. The next session gives the runtime the bus it connected and carries
// on from there, so a Rack that has been serving traffic on its own is not stopped to
// join the Mixer.
type sessionCarry struct {
	runtime   *rt.Manager
	specMgr   manager.Manager
	scenarios *scenarioSession
	machineID uuid.UUID
	name      string
}

// take returns what the previous session left, and clears it. A runtime that belongs
// to another identity is stopped: the Rack is not the one that started it.
func (c *sessionCarry) take(machineID uuid.UUID, name string, logger *slog.Logger) (incoming sessionCarry, adopted bool) {
	incoming = *c
	*c = sessionCarry{}
	if incoming.runtime == nil {
		return sessionCarry{}, false
	}
	if incoming.machineID != machineID || incoming.name != name {
		logger.Info("The Rack's identity changed: stopping the gears the previous session left running")
		incoming.runtime.Shutdown()
		return sessionCarry{}, false
	}
	return incoming, true
}

// mixerPublicKey returns the Mixer's key from the Rack's passport, or nil when there
// is no passport that verifies.
func mixerPublicKey(statePath string) []byte {
	env, err := pki.LoadStateEnvelope(statePath)
	if err != nil {
		return nil
	}
	state, err := env.Verify()
	if err != nil {
		return nil
	}
	return state.MixerPublic
}
