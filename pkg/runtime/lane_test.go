// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func testLane(queueSize int, sendTimeout time.Duration) *localLane {
	return newLocalLane(queueSize, sendTimeout, slog.Default)
}

func laneMsg(seq int) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.RawPayload = []byte(fmt.Sprintf("seq-%d", seq))
	m.Metadata["seq"] = fmt.Sprint(seq)
	return m
}

// collector gathers what a handler receives.
type collector struct {
	mu   sync.Mutex
	msgs []*fluxmsg.FluxMsg
}

func (c *collector) handle(_ context.Context, m *fluxmsg.FluxMsg) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func (c *collector) seqs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.msgs))
	for i, m := range c.msgs {
		out[i] = m.Metadata["seq"]
	}
	return out
}

func TestLane_DeliversInOrder(t *testing.T) {
	l := testLane(64, time.Second)
	c := &collector{}
	sub := l.subscribe("flux.msg.r.g.out", c.handle)
	defer func() { _ = sub.Unsubscribe() }()

	const n = 500
	var want []string
	for i := 0; i < n; i++ {
		_, err := l.publish(context.Background(), "flux.msg.r.g.out", laneMsg(i))
		require.NoError(t, err)
		want = append(want, fmt.Sprint(i))
	}

	require.NoError(t, l.quiesce(context.Background()))
	assert.Equal(t, want, c.seqs(), "a wire delivers in the order the gear emitted")
}

// Each subscriber gets a copy of its own: a gear that changes the message it was
// handed must not change what another gear, or the emitter, holds.
func TestLane_EachSubscriberGetsItsOwnCopy(t *testing.T) {
	l := testLane(8, time.Second)
	var mu sync.Mutex
	seen := map[string]string{}
	handler := func(name string) func(context.Context, *fluxmsg.FluxMsg) {
		return func(_ context.Context, m *fluxmsg.FluxMsg) {
			mu.Lock()
			seen[name] = m.Metadata["who"] // what the message said when it arrived
			mu.Unlock()
			m.Metadata["who"] = name // and then this gear changes it
			m.RawPayload[0] = 'X'
		}
	}
	a := l.subscribe("s", handler("a"))
	b := l.subscribe("s", handler("b"))
	defer func() { _ = a.Unsubscribe(); _ = b.Unsubscribe() }()

	original := laneMsg(1)
	original.Metadata["who"] = "emitter"
	n, err := l.publish(context.Background(), "s", original)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	require.NoError(t, l.quiesce(context.Background()))

	assert.Equal(t, map[string]string{"a": "emitter", "b": "emitter"}, seen, "neither saw the other's change")
	assert.Equal(t, "emitter", original.Metadata["who"], "the emitter's message is untouched")
	assert.Equal(t, byte('s'), original.RawPayload[0])
}

func TestLane_EmitterChangesAfterPublishDoNotReachTheHandler(t *testing.T) {
	l := testLane(8, time.Second)
	release := make(chan struct{})
	got := make(chan string, 1)
	sub := l.subscribe("s", func(_ context.Context, m *fluxmsg.FluxMsg) {
		<-release
		got <- string(m.RawPayload)
	})
	defer func() { _ = sub.Unsubscribe() }()

	m := laneMsg(1)
	_, err := l.publish(context.Background(), "s", m)
	require.NoError(t, err)
	m.RawPayload[0] = 'X' // the emitter reuses its buffer
	close(release)

	assert.Equal(t, "seq-1", <-got)
}

func TestLane_NoSubscriberMeansNothingDelivered(t *testing.T) {
	l := testLane(8, time.Second)

	n, err := l.publish(context.Background(), "nobody", laneMsg(1))

	require.NoError(t, err)
	assert.Zero(t, n)
}

// A consumer that does not keep up makes the emitter wait, and then fail with an
// error it can act on.
func TestLane_FullQueueFailsTheEmitterAfterTheTimeout(t *testing.T) {
	l := testLane(1, 50*time.Millisecond)
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	sub := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
	})
	defer func() { close(block); _ = sub.Unsubscribe() }()

	_, err := l.publish(context.Background(), "s", laneMsg(1))
	require.NoError(t, err)
	<-entered // the first message is in the handler, which is stuck
	_, err = l.publish(context.Background(), "s", laneMsg(2))
	require.NoError(t, err, "the second one fits in the queue")

	start := time.Now()
	_, err = l.publish(context.Background(), "s", laneMsg(3))

	require.ErrorIs(t, err, ErrLaneFull)
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "it waited the whole timeout before giving up")
}

func TestLane_EmitterContextEndsTheWait(t *testing.T) {
	l := testLane(1, time.Minute)
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	sub := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
	})
	defer func() { close(block); _ = sub.Unsubscribe() }()
	_, _ = l.publish(context.Background(), "s", laneMsg(1))
	<-entered
	_, _ = l.publish(context.Background(), "s", laneMsg(2))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := l.publish(ctx, "s", laneMsg(3))

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestLane_UnsubscribeStopsDeliveryAndForgetsWhatWasQueued(t *testing.T) {
	l := testLane(16, time.Second)
	c := &collector{}
	sub := l.subscribe("s", c.handle)
	_, _ = l.publish(context.Background(), "s", laneMsg(1))
	require.NoError(t, l.quiesce(context.Background()))

	require.NoError(t, sub.Unsubscribe())
	require.NoError(t, sub.Unsubscribe(), "a second call is harmless")

	n, err := l.publish(context.Background(), "s", laneMsg(2))
	require.NoError(t, err)
	assert.Zero(t, n, "nobody is subscribed any more")
	assert.Equal(t, 1, c.count())
	assert.Zero(t, l.pending.Load())
}

func TestLane_QuiesceWaitsForWhatIsInFlight(t *testing.T) {
	l := testLane(16, time.Second)
	release := make(chan struct{})
	sub := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) { <-release })
	defer func() { _ = sub.Unsubscribe() }()
	_, _ = l.publish(context.Background(), "s", laneMsg(1))
	_, _ = l.publish(context.Background(), "s", laneMsg(2))

	short, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	err := l.quiesce(short)
	cancel()
	require.Error(t, err, "two messages are still there")
	assert.Contains(t, err.Error(), "still queued")

	close(release)
	require.NoError(t, l.quiesce(context.Background()))
}

func TestLane_HandlerPanicKeepsTheSubscriptionAlive(t *testing.T) {
	l := testLane(16, time.Second)
	c := &collector{}
	sub := l.subscribe("s", func(ctx context.Context, m *fluxmsg.FluxMsg) {
		if m.Metadata["seq"] == "1" {
			panic("gear blew up")
		}
		c.handle(ctx, m)
	})
	defer func() { _ = sub.Unsubscribe() }()

	_, _ = l.publish(context.Background(), "s", laneMsg(1))
	_, _ = l.publish(context.Background(), "s", laneMsg(2))
	require.NoError(t, l.quiesce(context.Background()))

	assert.Equal(t, []string{"2"}, c.seqs(), "the message after the panic still arrives")
}

type ctxKey struct{}

// The handler keeps what the emitter's context carried (the trace) but is not
// cancelled when the emitter's context ends.
func TestLane_HandlerContextKeepsValuesAndSurvivesTheEmitter(t *testing.T) {
	l := testLane(8, time.Second)
	release := make(chan struct{})
	type result struct {
		value any
		err   error
	}
	got := make(chan result, 1)
	sub := l.subscribe("s", func(ctx context.Context, _ *fluxmsg.FluxMsg) {
		<-release
		got <- result{ctx.Value(ctxKey{}), ctx.Err()}
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "span-1"))
	_, err := l.publish(ctx, "s", laneMsg(1))
	require.NoError(t, err)
	cancel() // the emitter is done
	close(release)

	r := <-got
	assert.Equal(t, "span-1", r.value)
	assert.NoError(t, r.err, "the emitter finishing does not cancel the gear that is handling its message")
}

// The lane refuses what the bus would refuse, so moving a wire onto it does not let
// an invalid message through.
func TestLane_RefusesAnInvalidMessage(t *testing.T) {
	l := testLane(8, time.Second)
	sub := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {})
	defer func() { _ = sub.Unsubscribe() }()

	bad := fluxmsg.New()
	bad.Metadata["k"] = string([]byte{0xff, 0xfe})

	_, err := l.publish(context.Background(), "s", bad)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")
}

// Before this fix, publish stopped at the first subscriber whose queue was
// full, so a stuck consumer denied the message to every subscriber after it
// too, even a healthy one with room to spare.
func TestLane_PublishReachesHealthySubscribersDespiteOneStuckOne(t *testing.T) {
	l := testLane(1, 30*time.Millisecond)
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	stuck := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
	})
	healthy := &collector{}
	sub := l.subscribe("s", healthy.handle)
	defer func() { close(block); _ = stuck.Unsubscribe(); _ = sub.Unsubscribe() }()

	// First message: pulled straight into the stuck handler, which then blocks;
	// its queue is empty again. Second: fills that now-empty one-slot queue.
	// Third: the queue is full and the send times out.
	_, err := l.publish(context.Background(), "s", laneMsg(0))
	require.NoError(t, err)
	<-entered // the handler is now stuck on message 0

	_, err = l.publish(context.Background(), "s", laneMsg(1))
	require.NoError(t, err, "fits in the now-empty queue slot")

	n, err := l.publish(context.Background(), "s", laneMsg(2))
	require.ErrorIs(t, err, ErrLaneFull, "the stuck subscriber's failure is still reported")
	assert.Equal(t, 1, n, "but the healthy subscriber is counted as delivered")

	require.Eventually(t, func() bool { return healthy.count() == 3 }, time.Second, 10*time.Millisecond,
		"the healthy subscriber must receive every message despite the other one being stuck")
}

// A handler that never returns must not make Unsubscribe wait forever:
// Unsubscribe is called from Manager.stopAll while m.mu is held, so an
// unbounded wait here would deadlock every future ApplyScenario/Drain/
// Shutdown on the Rack over one stuck gear.
func TestLane_UnsubscribeGivesUpAfterTheWaitInsteadOfBlockingForever(t *testing.T) {
	original := unsubscribeWait
	unsubscribeWait = 50 * time.Millisecond
	defer func() { unsubscribeWait = original }()

	l := testLane(1, time.Second)
	entered := make(chan struct{}, 1)
	neverReturns := make(chan struct{}) // deliberately never closed
	sub := l.subscribe("s", func(context.Context, *fluxmsg.FluxMsg) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-neverReturns
	})

	_, err := l.publish(context.Background(), "s", laneMsg(1))
	require.NoError(t, err)
	<-entered // the handler is now stuck for good

	done := make(chan error, 1)
	go func() { done <- sub.Unsubscribe() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe blocked well past unsubscribeWait: a stuck handler would deadlock stopAll")
	}
}

func TestLane_Configure(t *testing.T) {
	l := testLane(0, 0)
	assert.Equal(t, defaultLaneQueueSize, l.queueSize)
	assert.Equal(t, defaultLaneSendTimeout, l.sendTimeout)

	l.configure(7, time.Second)
	assert.Equal(t, 7, l.queueSize)
	assert.Equal(t, time.Second, l.sendTimeout)
}
