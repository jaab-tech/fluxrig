// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
)

type fakeApplier struct {
	applied  []*registry.Scenario
	err      error
	needsBus bool
}

func (f *fakeApplier) NeedsBus(*registry.Scenario) bool { return f.needsBus }

func (f *fakeApplier) ApplyScenario(_ context.Context, sc *registry.Scenario) error {
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, sc)
	return nil
}

const savedScenarioYAML = "meta:\n  name: payment-flow\n  version: 1.2.0\ngears: []\n"

func savedStore(t *testing.T, machineID uuid.UUID, rack string, scenario string) *rt.LocalScenarioStore {
	t.Helper()
	store := rt.NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))
	require.NoError(t, store.Save(&fluxmsg.ScenarioPayload{
		Version: "1.2.0", Name: "payment-flow", RackName: rack, MachineID: machineID, Scenario: []byte(scenario),
	}))
	return store
}

func TestResumeLocalScenario_AppliesTheSavedCopy(t *testing.T) {
	id := uuid.New()
	store := savedStore(t, id, "rack-1", savedScenarioYAML)
	applier := &fakeApplier{}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, id, "rack-1", false, false)

	assert.Equal(t, []byte(savedScenarioYAML), got, "the caller compares this with what the Mixer sends")
	require.Len(t, applier.applied, 1)
	assert.Equal(t, "payment-flow", applier.applied[0].Meta.Name)
}

// The Mixer's scenario is newer than any copy: a copy applied after it would
// put the Rack back on an old scenario.
func TestResumeLocalScenario_YieldsToTheMixer(t *testing.T) {
	id := uuid.New()
	store := savedStore(t, id, "rack-1", savedScenarioYAML)
	applier := &fakeApplier{}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, id, "rack-1", true, false)

	assert.Nil(t, got)
	assert.Empty(t, applier.applied)
}

func TestResumeLocalScenario_NothingToResume(t *testing.T) {
	store := rt.NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))
	applier := &fakeApplier{}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, uuid.New(), "rack-1", false, false)

	assert.Nil(t, got)
	assert.Empty(t, applier.applied)
}

func TestResumeLocalScenario_RefusesAnotherRacksCopy(t *testing.T) {
	store := savedStore(t, uuid.New(), "rack-1", savedScenarioYAML)
	applier := &fakeApplier{}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, uuid.New(), "rack-1", false, false)

	assert.Nil(t, got)
	assert.Empty(t, applier.applied)
}

func TestResumeLocalScenario_ApplyFailureKeepsTheCopy(t *testing.T) {
	id := uuid.New()
	store := savedStore(t, id, "rack-1", savedScenarioYAML)
	applier := &fakeApplier{err: errors.New("convergence timeout")}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, id, "rack-1", false, false)

	assert.Nil(t, got)
	saved, err := store.Load(id, "rack-1")
	require.NoError(t, err)
	assert.NotNil(t, saved, "a failed resume must not throw the copy away: the Mixer's scenario may still work")
}

func TestResumeLocalScenario_UnparseableCopy(t *testing.T) {
	id := uuid.New()
	store := savedStore(t, id, "rack-1", "gears: [unclosed")
	applier := &fakeApplier{}

	got := resumeLocalScenario(context.Background(), slog.Default(), store, applier, id, "rack-1", false, false)

	assert.Nil(t, got)
	assert.Empty(t, applier.applied)
}

func sessionWith(t *testing.T, store *rt.LocalScenarioStore) *scenarioSession {
	t.Helper()
	return &scenarioSession{store: store}
}

// The Mixer sends its scenario a moment after a resume. The same scenario must
// leave the gears alone; a different one must replace them.
func TestScenarioSession_ConsumeResumed(t *testing.T) {
	id := uuid.New()
	s := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	applier := &fakeApplier{}

	require.True(t, s.resume(context.Background(), slog.Default(), applier, id, "rack-1"))
	assert.True(t, s.consumeResumed([]byte(savedScenarioYAML)), "same scenario: keep the gears running")
	assert.False(t, s.consumeResumed([]byte(savedScenarioYAML)), "the marker is spent by the first push")

	s2 := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	require.True(t, s2.resume(context.Background(), slog.Default(), &fakeApplier{}, id, "rack-1"))
	assert.False(t, s2.consumeResumed([]byte("meta:\n  name: other\n")), "a different scenario replaces the resumed one")
}

// Once the Mixer has sent a scenario, an older copy must never be resumed.
func TestScenarioSession_MixerScenarioBlocksResume(t *testing.T) {
	id := uuid.New()
	s := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	applier := &fakeApplier{}

	s.mu.Lock()
	s.consumeResumed([]byte("meta:\n  name: newer\n"))
	s.mu.Unlock()

	assert.False(t, s.resume(context.Background(), slog.Default(), applier, id, "rack-1"))
	assert.Empty(t, applier.applied)
}

func TestScenarioSession_WithoutAStore(t *testing.T) {
	s := &scenarioSession{}
	applier := &fakeApplier{}

	assert.False(t, s.resume(context.Background(), slog.Default(), applier, uuid.New(), "rack-1"))
	s.save(slog.Default(), &fluxmsg.ScenarioPayload{Scenario: []byte("x")}) // must not panic
	s.announceWaiting(slog.Default(), uuid.New(), "rack-1")                 // must not panic
	assert.Empty(t, applier.applied)
}

func TestScenarioSession_SaveThenResume(t *testing.T) {
	id := uuid.New()
	store := rt.NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))
	s := sessionWith(t, store)

	s.applied(slog.Default(), &fluxmsg.ScenarioPayload{
		Version: "3.0.0", Name: "payment-flow", RackName: "rack-1", MachineID: id, Scenario: []byte(savedScenarioYAML),
	})

	applier := &fakeApplier{}
	fresh := sessionWith(t, store) // a new session, as after a restart
	require.True(t, fresh.resume(context.Background(), slog.Default(), applier, id, "rack-1"))
	require.Len(t, applier.applied, 1)
	assert.Equal(t, "payment-flow", applier.applied[0].Meta.Name)
}

func TestOfflineStartBound(t *testing.T) {
	assert.Equal(t, 3*time.Second, offlineStartBound(uuid.New(), 3*time.Second), "a Rack with a passport gets the short window")
	assert.Equal(t, time.Duration(0), offlineStartBound(uuid.Nil, 3*time.Second), "one without keeps the full schedule")
}

// A Rack with no bus starts a scenario that needs none, and leaves alone one that does.
func TestScenarioSession_ResumeWithoutABus(t *testing.T) {
	id := uuid.New()

	internal := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	applier := &fakeApplier{}
	assert.True(t, internal.resumeWithoutBus(context.Background(), slog.Default(), applier, id, "rack-1"))
	require.Len(t, applier.applied, 1, "a scenario whose wires stay inside the Rack starts without the bus")

	needy := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	needsBus := &fakeApplier{needsBus: true}
	assert.False(t, needy.resumeWithoutBus(context.Background(), slog.Default(), needsBus, id, "rack-1"))
	assert.Empty(t, needsBus.applied, "a scenario that uses the bus waits for it, whole, and is not half applied")
	assert.Nil(t, needy.resumed)
}

// The next session finds the scenario already running: it must not start it again,
// which would stop the gears the previous session kept alive.
func TestScenarioSession_AScenarioAlreadyRunningIsNotStartedAgain(t *testing.T) {
	id := uuid.New()
	s := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	first := &fakeApplier{}
	require.True(t, s.resumeWithoutBus(context.Background(), slog.Default(), first, id, "rack-1"))

	again := &fakeApplier{}
	assert.True(t, s.resumeWithoutBus(context.Background(), slog.Default(), again, id, "rack-1"), "still counts as running")
	assert.False(t, s.resume(context.Background(), slog.Default(), again, id, "rack-1"), "and the online resume does nothing")
	assert.Empty(t, again.applied)
}

// The Mixer can send the scenario that already runs again: a push arrives at
// least once, a subscription that replaces its consumer redelivers it, and a
// Rack that reconnects gets it again. That must keep the gears running; a
// different scenario must not.
func TestScenarioSession_RunningScenarioIsNotAppliedAgain(t *testing.T) {
	same := func() *fluxmsg.ScenarioPayload {
		return &fluxmsg.ScenarioPayload{Scenario: []byte(savedScenarioYAML), Timestamp: 1}
	}
	s := &scenarioSession{}
	assert.False(t, s.keepsGears(same()), "nothing runs yet")

	s.applied(slog.Default(), same())
	assert.True(t, s.keepsGears(same()), "the same scenario again keeps the gears")
	assert.False(t, s.keepsGears(&fluxmsg.ScenarioPayload{Scenario: []byte("meta:\n  name: other\n")}), "a different scenario is applied")

	// A scenario resumed from the local copy and confirmed by the Mixer runs too.
	id := uuid.New()
	s2 := sessionWith(t, savedStore(t, id, "rack-1", savedScenarioYAML))
	require.True(t, s2.resume(context.Background(), slog.Default(), &fakeApplier{}, id, "rack-1"))
	require.True(t, s2.consumeResumed([]byte(savedScenarioYAML)))
	assert.True(t, s2.keepsGears(same()))
}

// An activation of the scenario that already runs is applied, which restarts
// the gears: that is how an operator restarts a gear that stopped. It is
// applied once, however often the push is delivered.
func TestScenarioSession_ActivationIsAppliedOnce(t *testing.T) {
	s := &scenarioSession{}
	s.applied(slog.Default(), &fluxmsg.ScenarioPayload{Scenario: []byte(savedScenarioYAML), Timestamp: 1})

	activation := &fluxmsg.ScenarioPayload{Scenario: []byte(savedScenarioYAML), Timestamp: 2, Reapply: true}
	assert.False(t, s.keepsGears(activation), "an activation restarts the gears")
	s.applied(slog.Default(), activation)
	assert.True(t, s.keepsGears(activation), "its redelivery does not")
	assert.True(t, s.keepsGears(&fluxmsg.ScenarioPayload{Scenario: []byte(savedScenarioYAML), Timestamp: 3}),
		"nor does the push of a Rack that reconnects")
	assert.False(t, s.keepsGears(&fluxmsg.ScenarioPayload{Scenario: []byte(savedScenarioYAML), Timestamp: 4, Reapply: true}),
		"a new activation does")
}
