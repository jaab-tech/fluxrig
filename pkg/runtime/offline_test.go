// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// probeGear keeps the context it was started with and listens for control commands,
// as the ISO and Conductor gears do.
type probeGear struct {
	MockGear
	ctx      sdk.GearContext
	commands <-chan ctrl.Command
}

func (g *probeGear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	if cp, ok := ctx.ControlPlane().(ctrl.ControlPlane); ok && cp != nil {
		ch, err := cp.Subscribe(ctx.GearName())
		if err != nil {
			return err
		}
		g.commands = ch
	}
	return nil
}

func newOfflineManager(t *testing.T, b bus.Bus) (*Manager, *probeGear) {
	t.Helper()
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "rack-a", b, gen, &MockManager{}, time.Second, 300*time.Millisecond, 20*time.Millisecond, false, false, nil)
	probe := &probeGear{}
	mgr.factory.Register("probe", func() sdk.NativeGear { return probe })
	return mgr, probe
}

func singleGear() *registry.Scenario {
	return &registry.Scenario{
		Meta:  registry.ScenarioMeta{Name: "single", Version: "1.0"},
		Gears: []registry.GearSpec{{Name: "probe-1", Type: "probe", Deploy: "rack-a"}},
	}
}

// A Rack that starts without the Mixer has a bus that never connected. A scenario that
// does not need one applies on it, and does not panic on the connection it lacks.
func TestManager_AppliesAScenarioOnABusThatNeverConnected(t *testing.T) {
	disconnected := bus.NewNatsBus("flux-msg")
	mgr, probe := newOfflineManager(t, disconnected)
	defer mgr.Shutdown()

	require.NoError(t, mgr.ApplyScenario(context.Background(), singleGear()))

	require.NotNil(t, probe.commands, "the gear got a control plane although there is no bus")
	assert.False(t, mgr.NeedsBus(singleGear()))
}

// Without a bus the gears of the Rack still signal each other in memory.
func TestManager_ControlCommandsStayInsideTheRackWithoutABus(t *testing.T) {
	mgr, probe := newOfflineManager(t, bus.NewNatsBus("flux-msg"))
	defer mgr.Shutdown()
	require.NoError(t, mgr.ApplyScenario(context.Background(), singleGear()))

	cp := probe.ctx.ControlPlane().(ctrl.ControlPlane)
	require.NoError(t, cp.Publish("probe-1", ctrl.Command{Cmd: "conn.close", Src: "codec"}))

	select {
	case got := <-probe.commands:
		assert.Equal(t, "conn.close", got.Cmd)
	case <-time.After(2 * time.Second):
		t.Fatal("the command never arrived")
	}
}

// The Mixer returns: the Rack hands the running gears a connected bus. The gear that
// captured its context at start uses the new bus, and the commands the Mixer sends
// reach it, with nothing started again.
func TestManager_SetBusReachesGearsThatAreAlreadyRunning(t *testing.T) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port: -1, ClusterName: "rejoin-test", StoreDir: t.TempDir(),
		StreamName: "flux-msg", StreamSubjects: []string{"flux.msg.>", "flux.ctrl.>"},
	})
	require.NoError(t, err)
	defer s.Shutdown()

	mgr, probe := newOfflineManager(t, bus.NewNatsBus("flux-msg"))
	defer mgr.Shutdown()
	require.NoError(t, mgr.ApplyScenario(context.Background(), singleGear()))

	// Before: the gear's bus is the one that never connected.
	err = probe.ctx.Bus().Publish(context.Background(), "flux.msg.rack-a.x.out", fluxmsg.New())
	require.Error(t, err)

	connected := bus.NewNatsBus("flux-msg")
	require.NoError(t, connected.Connect(s.ClientURL(), bus.ConnectOptions{Name: "rack-a"}))
	defer connected.Close()
	mgr.SetBus(connected)

	// After: the same context publishes on the connected bus.
	require.NoError(t, probe.ctx.Bus().Publish(context.Background(), "flux.msg.rack-a.x.out", fluxmsg.New()))

	// And a command from the Mixer's side reaches the gear that subscribed while offline.
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	require.NoError(t, ctrl.NewNATSControlPlane(nc).Publish("probe-1", ctrl.Command{Cmd: "sim.start", Src: "mixer"}))

	select {
	case got := <-probe.commands:
		assert.Equal(t, "sim.start", got.Cmd)
	case <-time.After(5 * time.Second):
		t.Fatal("the Mixer's command never reached the running gear")
	}
}

func TestBusRef_WithoutABusFailsWithoutPanicking(t *testing.T) {
	ref := newBusRef(nil)

	assert.ErrorIs(t, ref.Publish(context.Background(), "s", fluxmsg.New()), errNoBus)
	_, err := ref.Subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {})
	assert.ErrorIs(t, err, errNoBus)
	assert.Nil(t, ref.KV())
	assert.Nil(t, ref.Core())
	assert.NoError(t, ref.Purge(context.Background()))
	ref.Close()
}
