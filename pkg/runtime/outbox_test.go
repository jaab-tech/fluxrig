// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// downBus is a bus whose publish always fails, standing in for a bus that is
// down. It embeds the package mock for the rest of the interface.
type downBus struct {
	*bus.MockBus
	err error
}

func (b *downBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	return b.err
}

// orderBus is a working bus that records arrivals in order.
type orderBus struct {
	*bus.MockBus
	mu       sync.Mutex
	arrivals []string
	msgs     []*fluxmsg.FluxMsg
}

func (b *orderBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arrivals = append(b.arrivals, subject)
	b.msgs = append(b.msgs, msg)
	return nil
}

func (b *orderBus) arrived() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.arrivals...)
}

func newOutboxManager(t *testing.T, b bus.Bus) *Manager {
	t.Helper()
	gen, err := idgen.New(uuid.New())
	require.NoError(t, err)
	mgr := NewManager(uuid.New(), "test-rack", b, gen, &MockManager{}, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)
	require.NoError(t, mgr.SetOutboxConfig(100, 2, ""))
	return mgr
}

// busSubject marks a subject as bus-bound, as ApplyScenario would for a wire
// that leaves the Rack.
func busSubject(mgr *Manager, subject string) {
	mgr.portsMu.Lock()
	defer mgr.portsMu.Unlock()
	mgr.busSubjects[subject] = true
}

func testMsg(payload string) *fluxmsg.FluxMsg {
	msg := fluxmsg.New()
	msg.Data = map[string]any{"v": payload}
	return msg
}

// An emission to a bus-bound subject while the bus is down must be accepted,
// not returned as an error, and must sit in the outbox in order.
func TestOutbox_ParksOnBusFailure(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	for _, v := range []string{"a", "b", "c"} {
		require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg(v)))
	}
	assert.Equal(t, 3, mgr.outbox.len())
}

// SetBus with a working bus forwards everything parked, in order. On the old
// code the emit above returned the error and nothing was parked, so nothing
// could ever arrive here.
func TestOutbox_DrainsOnRejoin(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	payloads := []string{"one", "two", "three"}
	for _, v := range payloads {
		require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg(v)))
	}
	require.Equal(t, 3, mgr.outbox.len())

	up := &orderBus{MockBus: bus.NewMockBus()}
	mgr.SetBus(up)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.outbox.len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 0, mgr.outbox.len(), "the drain must forward everything parked")

	got := up.arrived()
	require.Len(t, got, 3)
	for _, s := range got {
		assert.Equal(t, subject, s)
	}
	for i, m := range up.msgs {
		assert.Equal(t, payloads[i], m.Data["v"], "drain must preserve order")
	}
}

// When full the oldest entry is dropped and counted; the gear is never
// blocked.
func TestOutbox_CapDropsOldest(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(3, 2, ""))
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	for _, v := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg(v)))
	}
	waitOutbox(t, mgr, 3)
	assert.Equal(t, uint64(2), mgr.outbox.dropped)

	up := &orderBus{MockBus: bus.NewMockBus()}
	mgr.SetBus(up)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.outbox.len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, up.arrived(), 3)
	assert.Equal(t, "c", up.msgs[0].Data["v"])
	assert.Equal(t, "e", up.msgs[2].Data["v"])
}

// A bus that fails the first drain attempt and then works still delivers: the
// drain stops with everything parked and the next SetBus finishes it.
func TestOutbox_DeadBusPausesDrain(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("x")))
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("y")))
	require.Equal(t, 2, mgr.outbox.len())

	// A SetBus with another dead bus: the drain must stop with both parked,
	// not drop them.
	mgr.SetBus(&downBus{MockBus: bus.NewMockBus(), err: errors.New("still down")})
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, 2, mgr.outbox.len(), "a dead bus must pause the drain, not eat the queue")
	assert.Equal(t, uint64(0), mgr.outbox.dropped)

	up := &orderBus{MockBus: bus.NewMockBus()}
	mgr.SetBus(up)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.outbox.len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, up.arrived(), 2)
}

// A message with no bus at all was never a deferred delivery: the old
// errNoBus error still returns.
func TestOutbox_NilBusStillErrors(t *testing.T) {
	mgr := newOutboxManager(t, nil)
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	err := mgr.publishPort(context.Background(), "g", "out", testMsg("x"))
	require.Error(t, err)
	assert.Equal(t, 0, mgr.outbox.len())
}

// selectiveBus rejects only a specific payload, standing in for a poison
// message on an otherwise healthy bus.
type selectiveBus struct {
	*bus.MockBus
	mu       sync.Mutex
	reject   string
	arrivals []string
	msgs     []*fluxmsg.FluxMsg
}

func (b *selectiveBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	v, _ := msg.Data["v"].(string)
	if v == b.reject {
		return fmt.Errorf("%w: this specific message is malformed", bus.ErrInvalidMessage)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arrivals = append(b.arrivals, subject)
	b.msgs = append(b.msgs, msg)
	return nil
}

// A poison message must not wedge the healthy messages behind it. The bus
// marks it as invalid, so the drain drops it at once and forwards the rest,
// with subject order on (the default): a message that can never be sent does
// not hold its subject.
func TestOutbox_PoisonHeadDoesNotWedgeHealthyTail(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(100, 1, ""))
	subject := "flux.msg.test-rack.g.out"
	busSubject(mgr, subject)

	// Park poison first, then healthy, while the bus is down.
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("poison")))
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("healthy")))
	require.Equal(t, 2, mgr.outbox.len())

	// A genuinely healthy bus that rejects only the poison payload.
	up := &selectiveBus{MockBus: bus.NewMockBus(), reject: "poison"}

	deadline := time.Now().Add(10 * time.Second)
	mgr.SetBus(up)
	for time.Now().Before(deadline) {
		if mgr.outbox.len() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	up.mu.Lock()
	arrived := append([]string(nil), up.arrivals...)
	up.mu.Unlock()
	require.Len(t, arrived, 1, "the healthy message behind the poison one must be delivered")
	assert.Equal(t, "healthy", up.msgs[0].Data["v"])
	assert.Equal(t, 0, mgr.outbox.len(), "the poison message must eventually be dropped, not kept forever")
	assert.Equal(t, uint64(1), mgr.outbox.dropped)
}

// waitOutbox polls until no drain is running and the outbox holds want
// messages, or fails at the deadline.
func waitOutbox(t *testing.T, mgr *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mgr.outbox.mu.Lock()
		done := !mgr.outbox.draining && mgr.outbox.items.Len() == want
		mgr.outbox.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("outbox did not settle at %d parked (now %d)", want, mgr.outbox.len())
}

// refusingBus refuses one payload a fixed number of times with an error that
// says nothing about the message (a transient refusal), then takes it. Every
// other payload goes through. It records arrivals in order.
type refusingBus struct {
	*bus.MockBus
	mu       sync.Mutex
	refuse   string
	times    int
	arrivals []string
}

func (b *refusingBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, _ := msg.Data["v"].(string)
	if v == b.refuse && b.times > 0 {
		b.times--
		return errors.New("stream temporarily unavailable")
	}
	b.arrivals = append(b.arrivals, v)
	return nil
}

func (b *refusingBus) arrived() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.arrivals...)
}

// parkAll emits each payload on its subject while the bus is down.
func parkAll(t *testing.T, mgr *Manager, emits [][2]string) {
	t.Helper()
	for _, e := range emits {
		require.NoError(t, mgr.publishPort(context.Background(), "g", e[0], testMsg(e[1])))
	}
	// An emission parked behind a held subject starts a drain against the
	// dead bus. Let it give up, so the bus the test installs next sees a
	// clean start.
	waitOutbox(t, mgr, len(emits))
}

// With subject order (the default), a message the bus refuses for a pass
// holds the rest of its subject: the reversal parked after its authorization
// never reaches the bus first. Another subject is not held back.
func TestOutbox_SubjectOrderHoldsSubjectBehindRefusal(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(100, 1, OutboxOrderSubject))
	busSubject(mgr, "flux.msg.test-rack.g.auth")
	busSubject(mgr, "flux.msg.test-rack.g.other")

	parkAll(t, mgr, [][2]string{{"auth", "authorization"}, {"auth", "reversal"}, {"other", "unrelated"}})

	// The authorization is refused for one full pass (retries+1 attempts).
	up := &refusingBus{MockBus: bus.NewMockBus(), refuse: "authorization", times: 2}
	mgr.SetBus(up)
	waitOutbox(t, mgr, 0)

	assert.Equal(t, []string{"unrelated", "authorization", "reversal"}, up.arrived(),
		"the reversal must wait for its authorization; the other subject must not")
	assert.Equal(t, uint64(0), mgr.outbox.dropped)
}

// With no order, the same refusal lets the reversal overtake the
// authorization. The test pins the difference the option makes.
func TestOutbox_NoOrderLetsLaterMessagesOvertake(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(100, 1, OutboxOrderNone))
	busSubject(mgr, "flux.msg.test-rack.g.auth")

	parkAll(t, mgr, [][2]string{{"auth", "authorization"}, {"auth", "reversal"}})

	up := &refusingBus{MockBus: bus.NewMockBus(), refuse: "authorization", times: 2}
	mgr.SetBus(up)
	waitOutbox(t, mgr, 0)

	assert.Equal(t, []string{"reversal", "authorization"}, up.arrived())
}

// A dead bus never eats the queue, however many rejoins fail. A refusal says
// nothing about the message, so no count of refusals makes it poison.
func TestOutbox_RepeatedDeadRejoinsDropNothing(t *testing.T) {
	down := &downBus{MockBus: bus.NewMockBus(), err: errors.New("bus down")}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(100, 1, ""))
	busSubject(mgr, "flux.msg.test-rack.g.out")

	parkAll(t, mgr, [][2]string{{"out", "a"}, {"out", "b"}})

	for i := 0; i < 5; i++ {
		mgr.SetBus(&downBus{MockBus: bus.NewMockBus(), err: errors.New("still down")})
		waitOutbox(t, mgr, 2)
	}
	assert.Equal(t, uint64(0), mgr.outbox.dropped, "refusals must never drop a message")

	up := &orderBus{MockBus: bus.NewMockBus()}
	mgr.SetBus(up)
	waitOutbox(t, mgr, 0)
	require.Len(t, up.arrived(), 2)
}

// With subject order, a live emission on a subject that still has parked
// messages queues behind them instead of reaching the bus first. The bus came
// back inside the connection, with no SetBus: the emission itself starts the
// drain.
func TestOutbox_LiveEmissionQueuesBehindParked(t *testing.T) {
	flaky := &switchBus{MockBus: bus.NewMockBus()}
	mgr := newOutboxManager(t, flaky)
	busSubject(mgr, "flux.msg.test-rack.g.out")

	parkAll(t, mgr, [][2]string{{"out", "first"}})

	flaky.setUp(true)
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("second")))
	waitOutbox(t, mgr, 0)

	assert.Equal(t, []string{"first", "second"}, flaky.arrived())
}

// With no order, a live emission goes straight to the bus, and a success
// starts the drain of what was parked.
func TestOutbox_NoOrderLiveEmissionGoesFirstAndKicksDrain(t *testing.T) {
	flaky := &switchBus{MockBus: bus.NewMockBus()}
	mgr := newOutboxManager(t, flaky)
	require.NoError(t, mgr.SetOutboxConfig(100, 2, OutboxOrderNone))
	busSubject(mgr, "flux.msg.test-rack.g.out")

	parkAll(t, mgr, [][2]string{{"out", "first"}})

	flaky.setUp(true)
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("second")))
	waitOutbox(t, mgr, 0)

	assert.Equal(t, []string{"second", "first"}, flaky.arrived())
}

// An unknown order is an error and leaves the outbox as it was.
func TestOutbox_UnknownOrderRejected(t *testing.T) {
	mgr := newOutboxManager(t, nil)
	require.Error(t, mgr.SetOutboxConfig(100, 2, "fifo"))
	assert.True(t, mgr.outbox.ordered, "a rejected order must not change the default")
	require.NoError(t, mgr.SetOutboxConfig(100, 2, OutboxOrderNone))
	assert.False(t, mgr.outbox.ordered)
}

// switchBus refuses every publish while down and records arrivals while up.
type switchBus struct {
	*bus.MockBus
	mu       sync.Mutex
	up       bool
	arrivals []string
}

func (b *switchBus) setUp(up bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.up = up
}

func (b *switchBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.up {
		return errors.New("bus down")
	}
	v, _ := msg.Data["v"].(string)
	b.arrivals = append(b.arrivals, v)
	return nil
}

func (b *switchBus) arrived() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.arrivals...)
}
