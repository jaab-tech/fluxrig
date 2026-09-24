// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
)

// scenarioApplier is what a Rack needs from its runtime to start a scenario.
type scenarioApplier interface {
	ApplyScenario(ctx context.Context, sc *registry.Scenario) error
	// NeedsBus reports whether the scenario uses the bus for its wires.
	NeedsBus(sc *registry.Scenario) bool
}

// resumeLocalScenario applies the scenario the Rack last applied, from its own
// copy, and returns the YAML it applied. It returns nil when nothing was applied:
// no copy, a copy that belongs to another Rack, one that cannot be read, or the
// Mixer having already sent a scenario, which is newer than any copy.
//
// withoutBus says the Rack has no bus: the scenario is applied only if none of its
// wires needs one, and otherwise it waits for the bus.
//
// The caller holds the lock that serializes applying scenarios. A failure here is
// logged and leaves the copy alone: the Mixer's own scenario may still succeed.
func resumeLocalScenario(ctx context.Context, logger *slog.Logger, store *rt.LocalScenarioStore, applier scenarioApplier, machineID uuid.UUID, rackName string, mixerAlreadySent, withoutBus bool) []byte {
	if mixerAlreadySent {
		logger.Debug("Not resuming the saved scenario: the Mixer has already sent one")
		return nil
	}

	payload, err := store.Load(machineID, rackName)
	switch {
	case errors.Is(err, rt.ErrLocalScenarioMismatch):
		logger.Warn("Ignoring the saved scenario: it belongs to another rack", "error", err)
		return nil
	case err != nil:
		logger.Warn("Cannot read the saved scenario", "error", err)
		return nil
	case payload == nil:
		return nil
	}

	var sc registry.Scenario
	if err := yaml.Unmarshal(payload.Scenario, &sc); err != nil {
		logger.Error("Cannot parse the saved scenario", "error", err)
		return nil
	}

	if withoutBus && applier.NeedsBus(&sc) {
		logger.Info("The saved scenario uses the bus for its wires: it starts once the bus is reachable", "name", payload.Name, "version", payload.Version)
		return nil
	}

	if err := applier.ApplyScenario(ctx, &sc); err != nil {
		logger.Error("Failed to resume the saved scenario", "name", payload.Name, "version", payload.Version, "error", err)
		return nil
	}
	logger.Info("Resumed last scenario from local state", "name", payload.Name, "version", payload.Version)
	return payload.Scenario
}

// scenarioSession is one Rack session's view of its scenario: the local copy, and
// which scenario is running because of it. The mutex serializes applying a
// scenario, whether it comes from the Mixer, from the local copy or from a
// promotion, so a stale copy can never land on top of a newer scenario.
type scenarioSession struct {
	mu    sync.Mutex
	store *rt.LocalScenarioStore // nil when the Rack keeps no copy

	mixerSent bool   // the Mixer has sent a scenario this session, so no older copy may be resumed
	resumed   []byte // YAML resumed from the local copy, until the Mixer sends its own
}

// newScenarioSession keeps the copy where the configuration says, or none when
// base.scenario_file is empty.
func newScenarioSession(cfg *config.RackConfig) *scenarioSession {
	s := &scenarioSession{}
	if cfg.Base.ScenarioFile != "" {
		s.store = rt.NewLocalScenarioStore(filepath.Join(cfg.Base.StateDir, cfg.Base.ScenarioFile))
	}
	return s
}

// save keeps p as the Rack's copy. It is a no-op without a store, and a failure is
// logged: the scenario is already running, and only a later resume is lost.
func (s *scenarioSession) save(logger *slog.Logger, p *fluxmsg.ScenarioPayload) {
	if s.store == nil || p == nil {
		return
	}
	if err := s.store.Save(p); err != nil {
		logger.Warn("Failed to keep a local copy of the scenario", "error", err)
		return
	}
	logger.Debug("Local copy of the scenario saved", "path", s.store.Path(), "version", p.Version)
}

// consumeResumed is called when the Mixer sends scenario. It reports whether that
// is the one already resumed from the local copy, and forgets the marker either
// way: the Mixer's scenario replaces it. When they are the same, restarting the
// gears would only interrupt work that is already running. The caller holds mu.
func (s *scenarioSession) consumeResumed(scenario []byte) bool {
	same := s.resumed != nil && bytes.Equal(s.resumed, scenario)
	s.resumed = nil
	s.mixerSent = true
	return same
}

// applied keeps the Mixer's scenario p, which is now running, as the local copy.
func (s *scenarioSession) applied(logger *slog.Logger, p *fluxmsg.ScenarioPayload) {
	s.save(logger, p)
}

// resume applies the local copy, unless the Mixer has already sent a scenario,
// and reports whether it did. It takes mu.
func (s *scenarioSession) resume(ctx context.Context, logger *slog.Logger, applier scenarioApplier, machineID uuid.UUID, rackName string) bool {
	if s.store == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resumed != nil {
		return false // already running from the copy: a previous session started it and kept it
	}
	yamlApplied := resumeLocalScenario(ctx, logger, s.store, applier, machineID, rackName, s.mixerSent, false)
	if yamlApplied == nil {
		return false
	}
	s.resumed = yamlApplied
	return true
}

// resumeWithoutBus applies the local copy on a Rack that has no bus, and reports
// whether the scenario is running. It applies it only when the scenario needs no bus
// for its wires; one that does waits, and the Rack says so. A scenario that a
// previous session already started counts as running. It takes mu.
func (s *scenarioSession) resumeWithoutBus(ctx context.Context, logger *slog.Logger, applier scenarioApplier, machineID uuid.UUID, rackName string) bool {
	if s.store == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resumed != nil {
		return true
	}
	yamlApplied := resumeLocalScenario(ctx, logger, s.store, applier, machineID, rackName, s.mixerSent, true)
	if yamlApplied == nil {
		return false
	}
	s.resumed = yamlApplied
	return true
}

// announceWaiting says, for a Rack that started without a bus, that a saved
// scenario is waiting. Gears exchange their messages over the bus, so none of
// them can start until it is reachable, and an operator reading the log should
// not expect otherwise.
func (s *scenarioSession) announceWaiting(logger *slog.Logger, machineID uuid.UUID, rackName string) {
	if s.store == nil {
		return
	}
	p, err := s.store.Load(machineID, rackName)
	switch {
	case err != nil:
		logger.Warn("Ignoring the saved scenario", "error", err)
	case p != nil:
		logger.Warn("A saved scenario is waiting for the bus",
			"name", p.Name, "version", p.Version)
	}
}
