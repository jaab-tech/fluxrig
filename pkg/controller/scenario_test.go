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
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestScenarioController_Metadata(t *testing.T) {
	// 1. Setup
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)

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
	list, err := sc.List()
	if err != nil {
		t.Errorf("List failed: %v", err)
	}
	if len(list) != 1 || list[0] != name {
		t.Errorf("List mismatch: %v", list)
	}

	// 4. Before Activation
	if sc.GetActiveName() != "" {
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
	if sc.GetActiveName() != name {
		t.Errorf("Active name mismatch: %s", sc.GetActiveName())
	}
	if sc.CurrentVersion() != "1.0.0" {
		t.Errorf("Version mismatch: %s", sc.CurrentVersion())
	}
	if sc.GetActiveScenario() == nil {
		t.Error("Active scenario is nil")
	}
}

func TestScenarioController_ComplexActivation(t *testing.T) {
	store, _ := duckdb.NewStore(slog.Default(), ":memory:")
	_ = store.Migrate(context.Background())
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)
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

// Import files a scenario whose Racks have not enrolled; Activate refuses it with
// a typed error until they have, and the scenario stays imported throughout.
func TestScenarioController_ActivateNeedsEnrolledRacks(t *testing.T) {
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), t.TempDir(), store, gen, mixerID, time.Second)
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
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), t.TempDir(), store, gen, mixerID, time.Second)
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
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if errMig := store.Migrate(context.Background()); errMig != nil {
		t.Fatal(errMig)
	}

	mockBus := &MockScenarioBus{}
	tmpDir := t.TempDir()

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)

	sc := NewScenarioController(slog.Default(), tmpDir, store, gen, mixerID, time.Second)
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
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), t.TempDir(), store, gen, mixerID, time.Second)
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
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	store.SetAutoAdopt(true)

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), t.TempDir(), store, gen, mixerID, time.Second)
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
	store, err := duckdb.NewStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), t.TempDir(), store, gen, mixerID, time.Second)
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
