// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func TestScenarioController_Metadata(t *testing.T) {
	// 1. Setup
	store, _ := newFixtureStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)

	ctx := context.Background()

	// 2. Import
	yamlData := []byte(`
meta:
  version: "1.0.0"
gears: []
wires: []
racks: []
`)
	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// 3. List
	list, err := sc.List(context.Background())
	if err != nil {
		t.Errorf("List failed: %v", err)
	}
	if len(list) != 1 || list[0] != name {
		t.Errorf("List mismatch: %v", list)
	}

	// 4. Before Activation
	if sc.GetActiveName(context.Background()) != "" {
		t.Error("Active name should be empty")
	}
	if sc.CurrentVersion() != "none" {
		t.Error("Current version should be none")
	}

	// 5. Activate
	if err := sc.Activate(ctx, name); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// 6. After Activation
	if sc.GetActiveName(context.Background()) != name {
		t.Errorf("Active name mismatch: %s", sc.GetActiveName(context.Background()))
	}
	if sc.CurrentVersion() != "1.0.0" {
		t.Errorf("Version mismatch: %s", sc.CurrentVersion())
	}
	if sc.GetActiveScenario() == nil {
		t.Error("Active scenario is nil")
	}
}

func TestScenarioController_ComplexActivation(t *testing.T) {
	store, _ := newFixtureStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(&MockScenarioBus{}) // prevent nil bus error

	ctx := context.Background()

	yamlData := []byte(`
meta:
  version: "2.0.0"
racks:
  - name: "rack-1"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
wires:
  - from: "gear-1.out"
    to: "gear-1.in"
`)
	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-register rack-1 (Simulate enrollment)
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	if _, err := store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID); err != nil {
		t.Fatalf("Failed to pre-register rack: %v", err)
	}

	// Activate triggers registerScenarioEntities
	if err := sc.Activate(ctx, name); err != nil {
		t.Fatal(err)
	}

	// Verify Registry
	if _, err := store.GetRackByName(ctx, "rack-1"); err != nil {
		t.Errorf("Rack used in scenario not registered/active: %v", err)
	}

	if _, err := store.GetEntityIDByName(ctx, "gear-1"); err != nil {
		t.Errorf("Gear not registered: %v", err)
	}
}

// Before this fix, a failure registering scenario entities in DuckDB was
// only logged: Activate still wrote the active pointer to disk and set the
// in-memory scenario, so the Mixer believed a scenario was active while its
// registry -- the source of truth for what is actually deployed -- never
// reflected it. A Rack declared under racks: that never enrolled makes
// registerScenarioEntities fail deterministically (waitForRack times out),
// while validateDeployTargets stays happy because no gear deploys to it.
func TestScenarioController_ActivateDoesNotCommitWhenRegistrationFails(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	// A short wait so the never-enrolling rack below times out quickly.
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Millisecond)

	ctx := context.Background()
	yamlData := []byte(`
meta:
  version: "1.0.0"
racks:
  - name: "ghost-rack"
gears: []
wires: []
`)
	name, err := sc.Import(ctx, yamlData, false)
	require.NoError(t, err)

	err = sc.Activate(ctx, name)
	require.Error(t, err, "activation must fail when the rack it declares never registered")

	assert.Empty(t, sc.GetActiveName(context.Background()), "the active pointer must not be committed when registration fails")
	assert.Nil(t, sc.GetActiveScenario())

}

// Import files a scenario whose Racks have not enrolled; Activate refuses it with
// a typed error until they have, and the scenario stays imported throughout.
func TestScenarioController_ActivateNeedsEnrolledRacks(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(&MockScenarioBus{})
	ctx := context.Background()

	name, err := sc.Import(ctx, []byte(`
meta:
  name: needs-rack
  version: "1.0.0"
racks:
  - name: "rack-1"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err, "import stays permissive")

	err = sc.Activate(ctx, name)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownDeployTarget)
	assert.Contains(t, err.Error(), "gear-1 deploys to unknown or inactive target 'rack-1'")
	assert.NotEqual(t, "needs-rack", sc.CurrentName(), "a refused activation must not make the scenario active")

	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)

	require.NoError(t, sc.Activate(ctx, name), "the same scenario activates once its Rack is enrolled")
	assert.Equal(t, "needs-rack", sc.CurrentName())
}

// A deploy target naming a Rack group the scenario itself declares gets its
// own, honest error: no Rack in the registry can carry labels yet, so a group
// can never be resolved here, and enrolling a Rack (unlike the plain-name
// case above) cannot fix it. This must not read as ErrUnknownDeployTarget,
// which invites exactly that wrong fix.
func TestScenarioController_ActivateNamesTheGapForAGroupTarget(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(&MockScenarioBus{})
	ctx := context.Background()

	name, err := sc.Import(ctx, []byte(`
meta:
  name: group-deploy
  version: "1.0.0"
racks:
  - group: "edge-eu"
    match:
      labels:
        region: "eu"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "edge-eu"
`), false)
	require.NoError(t, err, "import stays permissive")

	err = sc.Activate(ctx, name)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnresolvableGroupTarget)
	assert.NotErrorIs(t, err, ErrUnknownDeployTarget, "a group target is a different gap than a Rack that just needs to enroll")
	assert.Contains(t, err.Error(), `gear-1 deploys to group "edge-eu"`)
}

// Mock Scenario Bus (fluxmsg compatible)
type MockScenarioBus struct {
	PublishedTopic string
	PublishedMsg   *fluxmsg.FluxMsg
}

func (m *MockScenarioBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	m.PublishedTopic = subject
	m.PublishedMsg = msg
	return nil
}

func TestScenarioController_LifeCycle(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if errMig := store.Migrate(context.Background()); errMig != nil {
		t.Fatal(errMig)
	}

	mockBus := &MockScenarioBus{}

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)

	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(mockBus)

	ctx := context.Background()

	yamlData := []byte(`
meta:
  version: "1.0.0"
  description: "Test Scenario"
gears:
  - name: "test-gear"
    type: "native"
    deploy: "test-rack"
wires: []
racks:
  - name: "test-rack"
`)

	name, err := sc.Import(ctx, yamlData, false)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	if _, err := store.Register(ctx, uuid.New(), "test-rack", "sec", "ip", 80, "v1", nil, mixerID); err != nil {
		t.Fatalf("Failed to pre-register rack: %v", err)
	}

	if err := sc.Activate(ctx, name); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	active := sc.GetActiveScenario()
	if active == nil {
		t.Fatal("Active scenario is nil after activation")
	}

	if err := sc.PushActiveToRack(ctx, "test-rack"); err != nil {
		t.Errorf("PushActiveToRack failed: %v", err)
	}
}

// trackingScenarioBus records every subject a scenario was published to, so a test
// can tell which racks actually received the push, not only whether Activate erred.
type trackingScenarioBus struct{ subjects []string }

func (b *trackingScenarioBus) Publish(_ context.Context, subject string, _ *fluxmsg.FluxMsg) error {
	b.subjects = append(b.subjects, subject)
	return nil
}

// failingScenarioBus never delivers: every Publish fails, as if no Rack ever
// acknowledged the subject (or the bus itself were unreachable).
type failingScenarioBus struct{}

func (failingScenarioBus) Publish(context.Context, string, *fluxmsg.FluxMsg) error {
	return fmt.Errorf("simulated publish failure")
}

// A gear's deploy: target must receive the scenario even when racks: omits it (or is
// empty): before this test, targets came only from racks:, so this configuration
// reached nobody although validateDeployTargets passed it as well-formed.
func TestScenarioController_ActivatePushesToADeployPinNotListedInRacks(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	bus := &trackingScenarioBus{}
	sc.SetBus(bus)
	ctx := context.Background()

	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)

	// No top-level racks: at all, only a gear pinned to one.
	name, err := sc.Import(ctx, []byte(`
meta:
  name: pin-not-in-racks
  version: "1.0.0"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)

	require.NoError(t, sc.Activate(ctx, name), "a deploy pin is a valid target on its own")
	require.Len(t, bus.subjects, 1, "the pinned rack must receive exactly one push")
	assert.Contains(t, bus.subjects[0], "rack-1")
}

// Activate must not report success when the scenario reached none of its targets:
// that used to be a silent no-op, HTTP 200 included.
func TestScenarioController_ActivateFailsOnZeroDelivery(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(failingScenarioBus{})
	ctx := context.Background()

	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)

	name, err := sc.Import(ctx, []byte(`
meta:
  name: undeliverable
  version: "1.0.0"
racks:
  - name: "rack-1"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)

	err = sc.Activate(ctx, name)
	require.Error(t, err, "a scenario that reached no Rack must not be reported as activated")
	assert.ErrorIs(t, err, ErrNoRackReached)

	// The scenario is still the Mixer's bookkeeping of what is active: a Rack
	// that enrolls afterwards must still receive it through PushActiveToRack.
	// It is the caller of Activate, not this internal state, that must not be
	// told the push succeeded.
	assert.Equal(t, "undeliverable", sc.CurrentName(), "activation state is not rolled back")
}

// A scenario with nothing to deliver to (no racks:, no gear deploys) is a legitimate
// activation, not a delivery failure: TestScenarioController_Metadata already covers
// this with an empty scenario; this covers a scenario with global gears only.
func TestScenarioController_ActivateWithOnlyGlobalGearsSucceeds(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	sc.SetBus(failingScenarioBus{}) // must never be called: there is nowhere to push
	ctx := context.Background()

	name, err := sc.Import(ctx, []byte(`
meta:
  name: global-only
  version: "1.0.0"
gears:
  - name: "gear-1"
    type: "io_tcp"
`), false)
	require.NoError(t, err)

	require.NoError(t, sc.Activate(ctx, name), "a global gear needs no rack target")
}

// A scenario's own racks: list used to get every named Rack promoted from
// pending to active as a side effect of registerScenarioEntities, entirely
// outside the approval flow (fluxrig-explained/docs/17-deep-review.md,
// "Mixer, store and enrollment": scenario import auto-activates pending
// racks). The gear here has no deploy: pin naming the rack, so
// validateDeployTargets never touches it either: this exercises the
// registerScenarioEntities path specifically, not gear deploy validation.
func TestScenarioController_ActivateNeverPromotesAPendingRackNamedInRacks(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	store.SetBootstrapSecret("sec")
	_, err = store.Register(context.Background(), uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err, "SetAutoAdopt defaults false, so this Rack starts pending")

	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, 200*time.Millisecond)
	sc.SetBus(failingScenarioBus{}) // must never be called: nothing active to push to
	ctx := context.Background()

	name, err := sc.Import(ctx, []byte(`
meta:
  name: names-a-pending-rack
  version: "1.0.0"
racks:
  - name: "rack-1"
gears:
  - name: "gear-1"
    type: "io_tcp"
`), false)
	require.NoError(t, err)

	// Activate still reports the zero-delivery push (rack-1 is not an active
	// target), a pre-existing, separate behavior. What this test cares about
	// is the side effect on the registry, which the commit-then-report
	// design here applies regardless of that reported error.
	_ = sc.Activate(ctx, name)

	list, err := store.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "pending", list[0].Status, "importing a scenario must never promote a Rack it merely names")
}

// A restarted Mixer restores the active scenario from the registry: it
// reports it, rebuilds the topology rows, and pushes nothing to the Racks
// that already run it. A Rack that enrolls afterwards still receives it.
// Before the registry move the pointer file was written and never read, so a
// restart forgot the active scenario.
func TestScenarioController_LoadActiveRestoresWithoutPushing(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	ctx := context.Background()
	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)

	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)
	first := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	first.SetBus(&trackingScenarioBus{})
	name, err := first.Import(ctx, []byte(`
meta:
  name: restored
  version: "2.1.0"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)
	require.NoError(t, first.Activate(ctx, name))
	require.NoError(t, store.topo.ClearScenarioEntities(ctx), "a restart starts from empty topology rows")

	// The restart: a new controller over the same registry.
	bus := &trackingScenarioBus{}
	restarted := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	restarted.SetBus(bus)
	require.Equal(t, "none", restarted.CurrentName())

	require.NoError(t, restarted.LoadActive(ctx))
	assert.Equal(t, "restored", restarted.CurrentName())
	assert.Equal(t, "2.1.0", restarted.CurrentVersion())
	assert.Empty(t, bus.subjects, "restoring pushes nothing")
	_, err = store.topo.GetEntityIDByName(ctx, "gear-1")
	require.NoError(t, err, "the topology rows are rebuilt")

	require.NoError(t, restarted.PushActiveToRack(ctx, "rack-1"))
	require.Len(t, bus.subjects, 1, "a Rack that enrolls later still receives it")
}

func TestScenarioController_LoadActiveWithNothingActive(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	gen, _ := idgen.New(uuid.New())
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, uuid.New(), time.Second)
	require.NoError(t, sc.LoadActive(context.Background()))
	assert.Equal(t, "none", sc.CurrentName())
}

// payloadBus records the scenario bytes each push carries.
type payloadBus struct{ scenarios [][]byte }

func (b *payloadBus) Publish(_ context.Context, _ string, msg *fluxmsg.FluxMsg) error {
	p, err := fluxmsg.ParseScenarioPayload(msg.Data)
	if err != nil {
		return err
	}
	b.scenarios = append(b.scenarios, p.Scenario)
	return nil
}

// After a restart, the scenario a Rack receives must be byte-identical to
// the one it received at activation, entity IDs included. A Rack that resumed
// its own copy keeps its gears only on an exact match; a restored scenario
// registered afresh gets new IDs, and the Rack restarted its gears (the e2e
// start_without_mixer segment caught it).
func TestScenarioController_RestoredPushMatchesTheOriginal(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	ctx := context.Background()
	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)

	before := &payloadBus{}
	first := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	first.SetBus(before)
	name, err := first.Import(ctx, []byte(`
meta:
  name: same-bytes
  version: "1.0.0"
gears:
  - name: "gw"
    type: "io_tcp"
    deploy: "rack-1"
    config:
      delimiter: "\n"
  - name: "codec"
    type: "codec_iso8583"
    deploy: "rack-1"
wires:
  - from: "gw.out"
    to: "codec.in"
`), false)
	require.NoError(t, err)
	require.NoError(t, first.Activate(ctx, name))
	require.Len(t, before.scenarios, 1)
	require.NoError(t, store.topo.ClearScenarioEntities(ctx))

	after := &payloadBus{}
	restarted := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	restarted.SetBus(after)
	require.NoError(t, restarted.LoadActive(ctx))
	require.NoError(t, restarted.PushActiveToRack(ctx, "rack-1"))
	require.Len(t, after.scenarios, 1)
	assert.Equal(t, string(before.scenarios[0]), string(after.scenarios[0]))
}

// Restoring at startup never waits for a Rack: the Mixer cannot answer a
// Hello until startup ends, so waiting would keep out the Rack it waits for
// (the e2e io_tcp segment caught a 15s stall). A Rack that is gone leaves the
// scenario restored, with that Rack unresolved in the local rows.
func TestScenarioController_LoadActiveDoesNotWaitForAbsentRacks(t *testing.T) {
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")
	ctx := context.Background()
	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)

	first := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	first.SetBus(&trackingScenarioBus{})
	name, err := first.Import(ctx, []byte(`
meta:
  name: gone-rack
  version: "1.0.0"
racks:
  - name: rack-1
gears:
  - name: "gw"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)
	require.NoError(t, first.Activate(ctx, name))
	require.NoError(t, store.RemoveByName(ctx, "rack-1"))

	restarted := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, 10*time.Second)
	start := time.Now()
	require.NoError(t, restarted.LoadActive(ctx))
	assert.Less(t, time.Since(start), 2*time.Second, "restore must not wait out the Rack timeout")
	assert.Equal(t, "gone-rack", restarted.CurrentName())
}

// globalFixture is a controller over a fresh store with two active Racks and
// one pending, and a bus that records every push.
func globalFixture(t *testing.T) (*ScenarioController, *trackingScenarioBus) {
	t.Helper()
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetBootstrapSecret("sec")
	mixerID := uuid.New()
	ctx := context.Background()
	_, err = store.Register(ctx, uuid.New(), "rack-pending", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err, "auto-adopt is off: this Rack is pending")
	store.SetAutoAdopt(true)
	for _, n := range []string{"rack-1", "rack-2"} {
		_, err = store.Register(ctx, uuid.New(), n, "sec", "ip", 80, "v1", nil, mixerID)
		require.NoError(t, err)
	}
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, 200*time.Millisecond)
	bus := &trackingScenarioBus{}
	sc.SetBus(bus)
	return sc, bus
}

func pushesTo(bus *trackingScenarioBus, rack string) int {
	n := 0
	for _, s := range bus.subjects {
		if s == fluxmsg.SubjectScenarioPrefix+rack+fluxmsg.SubjectScenarioSuffix {
			n++
		}
	}
	return n
}

// A gear with no deploy target runs on every Rack, so activation pushes its
// scenario to every active Rack, once each. Before, it pushed to nobody, and
// a Rack received the scenario only if it enrolled after the activation.
func TestScenarioController_ActivatePushesAGlobalGearToEveryActiveRack(t *testing.T) {
	sc, bus := globalFixture(t)
	ctx := context.Background()
	name, err := sc.Import(ctx, []byte(`
meta:
  name: global-and-pinned
  version: "1.0.0"
gears:
  - name: "everywhere"
    type: "io_tcp"
  - name: "pinned"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)
	require.NoError(t, sc.Activate(ctx, name))

	assert.Equal(t, 1, pushesTo(bus, "rack-1"), "a Rack that is a pin and a global target gets one push")
	assert.Equal(t, 1, pushesTo(bus, "rack-2"))
	assert.Zero(t, pushesTo(bus, "rack-pending"), "a pending Rack gets nothing before it is approved")
}

// The push that follows an enrollment skips a Rack that the activation has
// reached after the enrollment began: a second push would restart its gears.
// A Rack that enrolls again later still gets the scenario.
func TestScenarioController_EnrollmentPushDoesNotRepeatTheActivation(t *testing.T) {
	sc, bus := globalFixture(t)
	ctx := context.Background()
	name, err := sc.Import(ctx, []byte(`
meta:
  name: global-only
  version: "1.0.0"
gears:
  - name: "everywhere"
    type: "io_tcp"
`), false)
	require.NoError(t, err)

	enrolledBefore := time.Now()
	require.NoError(t, sc.Activate(ctx, name))
	require.Equal(t, 1, pushesTo(bus, "rack-1"))

	// rack-1 enrolled, then the activation reached it: the enrollment push
	// that follows has nothing to add.
	require.NoError(t, sc.PushActiveToRackSince(ctx, "rack-1", enrolledBefore))
	assert.Equal(t, 1, pushesTo(bus, "rack-1"), "the enrollment push repeated the activation")

	// rack-1 enrolls again after the activation (a restart): it needs it.
	require.NoError(t, sc.PushActiveToRackSince(ctx, "rack-1", time.Now()))
	assert.Equal(t, 2, pushesTo(bus, "rack-1"), "a Rack that enrolled after the activation did not get the scenario")

	// A new activation is a new scenario to deliver, even to a Rack that
	// enrolled before it.
	require.NoError(t, sc.Activate(ctx, name))
	assert.Equal(t, 3, pushesTo(bus, "rack-1"))
}
