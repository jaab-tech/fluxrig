// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// deadBus is a bus that fails every call and counts them: a Rack whose scenario
// stays in its own memory must not make any.
type deadBus struct {
	calls atomic.Int64
}

var errDeadBus = errors.New("the bus is down")

func (d *deadBus) Connect(string, bus.ConnectOptions) error { d.calls.Add(1); return errDeadBus }
func (d *deadBus) Publish(context.Context, string, *fluxmsg.FluxMsg) error {
	d.calls.Add(1)
	return errDeadBus
}
func (d *deadBus) PublishRaw(context.Context, string, []byte, uuid.UUID) error {
	d.calls.Add(1)
	return errDeadBus
}
func (d *deadBus) Subscribe(string, bus.Handler) (bus.Subscription, error) {
	d.calls.Add(1)
	return nil, errDeadBus
}
func (d *deadBus) SubscribeRaw(string, string, bus.RawHandler) (bus.Subscription, error) {
	d.calls.Add(1)
	return nil, errDeadBus
}
func (d *deadBus) SubscribeDurable(string, string, bus.Handler) (bus.Subscription, error) {
	d.calls.Add(1)
	return nil, errDeadBus
}
func (d *deadBus) KV() bus.KeyValue { return nil }
func (d *deadBus) Core() any        { return nil }
func (d *deadBus) Close()           {}

// sourceGear emits what a test hands it, once started.
type sourceGear struct {
	MockGear
	emit chan func(*fluxmsg.FluxMsg)
}

func (g *sourceGear) Start(_ context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.emit <- emit
	return nil
}

// sinkGear records what reaches its input.
type sinkGear struct {
	MockGear
	mu    sync.Mutex
	got   []*fluxmsg.FluxMsg
	delay time.Duration
}

func (g *sinkGear) Process(_ context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	if g.delay > 0 {
		time.Sleep(g.delay)
	}
	g.mu.Lock()
	g.got = append(g.got, msg)
	g.mu.Unlock()
	return nil, nil
}

func (g *sinkGear) received() []*fluxmsg.FluxMsg {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*fluxmsg.FluxMsg(nil), g.got...)
}

func newLaneManager(t *testing.T, b bus.Bus, rack string) (*Manager, *sourceGear, *sinkGear) {
	t.Helper()
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, rack, b, gen, &MockManager{}, time.Second, 300*time.Millisecond, 20*time.Millisecond, false, false, nil)

	src := &sourceGear{emit: make(chan func(*fluxmsg.FluxMsg), 1)}
	sink := &sinkGear{}
	mgr.factory.Register("test_source", func() sdk.NativeGear { return src })
	mgr.factory.Register("test_sink", func() sdk.NativeGear { return sink })
	return mgr, src, sink
}

func twoGears(srcRack, sinkRack, lane string) *registry.Scenario {
	return &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "lanes", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "src", Type: "test_source", Deploy: srcRack},
			{Name: "dst", Type: "test_sink", Deploy: sinkRack},
		},
		Wires: []registry.WireSpec{{From: "src.out", To: "dst.in", Lane: lane}},
	}
}

func cardMessage() *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.RawPayload = []byte("0100 PAN=4111111111111111")
	return m
}

// The reason for the lane: a wire inside one Rack, by default, never reaches the
// bus. The bus here fails every call and the message still arrives.
func TestManager_WireInsideARackNeverTouchesTheBus(t *testing.T) {
	dead := &deadBus{}
	mgr, src, sink := newLaneManager(t, dead, "rack-a")
	defer mgr.Shutdown()

	require.NoError(t, mgr.ApplyScenario(context.Background(), twoGears("rack-a", "rack-a", "")))

	emit := <-src.emit
	emit(cardMessage())
	require.NoError(t, mgr.lane.quiesce(context.Background()))

	got := sink.received()
	require.Len(t, got, 1)
	assert.Equal(t, "0100 PAN=4111111111111111", string(got[0].RawPayload))
	assert.Zero(t, dead.calls.Load(), "the bus was not called at all")
	assert.False(t, mgr.NeedsBus(twoGears("rack-a", "rack-a", "")))
}

func TestManager_HotWireKeepsWorkingWhileTheBusIsDown(t *testing.T) {
	dead := &deadBus{}
	mgr, src, sink := newLaneManager(t, dead, "rack-a")
	defer mgr.Shutdown()
	require.NoError(t, mgr.ApplyScenario(context.Background(), twoGears("rack-a", "rack-a", registry.LaneHot)))

	emit := <-src.emit
	for i := 0; i < 50; i++ {
		emit(cardMessage())
	}
	require.NoError(t, mgr.lane.quiesce(context.Background()))

	assert.Len(t, sink.received(), 50)
	assert.Zero(t, dead.calls.Load())
}

// A wire that asks for the guaranteed lane is stored by the bus before the gear is
// told it was accepted, even inside one Rack.
func TestManager_GuaranteedWireInsideARackUsesTheBus(t *testing.T) {
	mock := &MockBus{}
	mgr, src, _ := newLaneManager(t, mock, "rack-a")
	defer mgr.Shutdown()
	sc := twoGears("rack-a", "rack-a", registry.LaneGuaranteed)
	require.NoError(t, mgr.ApplyScenario(context.Background(), sc))

	emit := <-src.emit
	emit(cardMessage())

	assert.Contains(t, mock.publishedSubjects(), "flux.msg.rack-a.src.out")
	assert.Contains(t, mock.handlers, "flux.msg.rack-a.src.out", "the consumer subscribed over the bus")
	assert.True(t, mgr.NeedsBus(sc))
}

// A wire to a gear on another Rack leaves through the bus, and the consumer is not
// on this Rack to subscribe here.
func TestManager_WireToAnotherRackLeavesThroughTheBus(t *testing.T) {
	mock := &MockBus{}
	mgr, src, sink := newLaneManager(t, mock, "rack-a")
	defer mgr.Shutdown()
	sc := twoGears("rack-a", "rack-b", "")
	require.NoError(t, mgr.ApplyScenario(context.Background(), sc))

	emit := <-src.emit
	emit(cardMessage())

	assert.Equal(t, []string{"flux.msg.rack-a.src.out"}, mock.publishedSubjects())
	assert.Empty(t, sink.received(), "the consumer runs on the other Rack")
	assert.True(t, mgr.NeedsBus(sc))
}

// A wire that arrives from another Rack is consumed over the bus, whatever the
// wire says.
func TestManager_WireFromAnotherRackArrivesOverTheBus(t *testing.T) {
	mock := &MockBus{}
	mgr, _, _ := newLaneManager(t, mock, "rack-b")
	defer mgr.Shutdown()
	sc := twoGears("rack-a", "rack-b", "")

	require.NoError(t, mgr.ApplyScenario(context.Background(), sc))

	assert.Contains(t, mock.handlers, "flux.msg.rack-a.src.out")
	assert.True(t, mgr.NeedsBus(sc))
}

// An emission nothing consumes is dropped, not stored.
func TestManager_EmissionWithNoConsumerIsNotPublished(t *testing.T) {
	mock := &MockBus{}
	mgr, src, _ := newLaneManager(t, mock, "rack-a")
	defer mgr.Shutdown()
	sc := &registry.Scenario{
		Meta:  registry.ScenarioMeta{Name: "lonely", Version: "1.0"},
		Gears: []registry.GearSpec{{Name: "src", Type: "test_source", Deploy: "rack-a"}},
	}
	require.NoError(t, mgr.ApplyScenario(context.Background(), sc))

	emit := <-src.emit
	emit(cardMessage())

	assert.Empty(t, mock.publishedSubjects())
}

// A stop that is asked to be graceful delivers what is queued between the gears.
func TestManager_DrainDeliversWhatIsQueuedOnTheLane(t *testing.T) {
	mgr, src, sink := newLaneManager(t, &deadBus{}, "rack-a")
	sink.delay = 20 * time.Millisecond
	defer mgr.Shutdown()
	require.NoError(t, mgr.ApplyScenario(context.Background(), twoGears("rack-a", "rack-a", "")))

	emit := <-src.emit
	for i := 0; i < 10; i++ {
		emit(cardMessage())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, mgr.Drain(ctx))

	assert.Len(t, sink.received(), 10, "nothing that was accepted is lost by a graceful stop")
}

func TestManager_LaneConfigReachesTheWires(t *testing.T) {
	mgr, _, _ := newLaneManager(t, &deadBus{}, "rack-a")
	defer mgr.Shutdown()

	mgr.SetLaneConfig(3, 250*time.Millisecond)

	assert.Equal(t, 3, mgr.lane.queueSize)
	assert.Equal(t, 250*time.Millisecond, mgr.lane.sendTimeout)
}

func TestScenarioNeedsBus(t *testing.T) {
	cases := []struct {
		name string
		sc   *registry.Scenario
		rack string
		want bool
	}{
		{"one Rack, default lane", twoGears("rack-a", "rack-a", ""), "rack-a", false},
		{"one Rack, guaranteed", twoGears("rack-a", "rack-a", registry.LaneGuaranteed), "rack-a", true},
		{"leaves for another Rack", twoGears("rack-a", "rack-b", ""), "rack-a", true},
		{"arrives from another Rack", twoGears("rack-a", "rack-b", ""), "rack-b", true},
		{"a Rack the scenario does not use", twoGears("rack-a", "rack-b", ""), "rack-c", false},
		{"both gears run everywhere", twoGears("", "", ""), "rack-a", false},
		{"a pinned source feeds a gear that runs everywhere", twoGears("rack-a", "", ""), "rack-a", true},
		{"a gear that runs everywhere feeds a pinned one", twoGears("", "rack-a", ""), "rack-a", false},
		{"no wires", &registry.Scenario{Gears: []registry.GearSpec{{Name: "g", Type: "x", Deploy: "rack-a"}}}, "rack-a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, scenarioNeedsBus(tc.sc, tc.rack, deployMap(tc.sc)))
		})
	}
}
