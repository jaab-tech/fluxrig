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
)

// eventBus records raw publishes in order, and refuses them while down is set.
type eventBus struct {
	*bus.MockBus
	down atomic.Bool
	mu   sync.Mutex
	got  []string
}

func (b *eventBus) PublishRaw(_ context.Context, _ string, data []byte, _ uuid.UUID) error {
	if b.down.Load() {
		return errors.New("bus down")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, string(data))
	return nil
}

func (b *eventBus) arrived() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

func publishTestEvent(t *testing.T, m *Manager, body string) error {
	t.Helper()
	return m.publishEvent(context.Background(), "flux.evt.conn.r.g", []byte(body), uuid.New())
}

func TestPublishEvent_ParksWhileTheBusIsDownAndLeavesInOrder(t *testing.T) {
	b := &eventBus{MockBus: bus.NewMockBus()}
	b.down.Store(true)
	m := newOutboxManager(t, b)

	for _, body := range []string{"e1", "e2", "e3"} {
		require.NoError(t, publishTestEvent(t, m, body), "a parked event is not lost")
	}
	assert.Equal(t, 3, m.outbox.len())

	b.down.Store(false)
	m.SetBus(b)
	require.Eventually(t, func() bool { return len(b.arrived()) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"e1", "e2", "e3"}, b.arrived())
	assert.Zero(t, m.outbox.len())
}

// A Rack that started without the Mixer has no bus at all. A message has no bus
// leg then, but the event is still owed.
func TestPublishEvent_ParksWithNoBusAtAll(t *testing.T) {
	m := newOutboxManager(t, nil)
	require.NoError(t, publishTestEvent(t, m, "e1"))
	assert.Equal(t, 1, m.outbox.len())

	b := &eventBus{MockBus: bus.NewMockBus()}
	m.SetBus(b)
	require.Eventually(t, func() bool { return len(b.arrived()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

// A NATS connection that reconnects by itself makes no SetBus, and an event has
// no traffic behind it. The retry loop is what sends it.
func TestPublishEvent_LeavesWhenTheBusReturnsWithoutSetBus(t *testing.T) {
	b := &eventBus{MockBus: bus.NewMockBus()}
	b.down.Store(true)
	m := newOutboxManager(t, b)
	require.NoError(t, publishTestEvent(t, m, "e1"))

	b.down.Store(false)
	require.Eventually(t, func() bool { return len(b.arrived()) == 1 }, 3*outboxEventRetry, 50*time.Millisecond)
	require.Eventually(t, func() bool { return !m.eventRetrying.Load() }, 3*outboxEventRetry, 50*time.Millisecond,
		"the loop must end when nothing is parked")
}

func TestPublishEvent_FullOutboxLosesTheEventAndEvictsNothing(t *testing.T) {
	b := &eventBus{MockBus: bus.NewMockBus()}
	b.down.Store(true)
	m := newOutboxManager(t, b)
	require.NoError(t, m.SetOutboxConfig(2, 1, ""))

	subject := "flux.msg.r.g.out"
	busSubject(m, subject)
	m.outbox.park(subject, testMsg("m1"))
	m.outbox.park(subject, testMsg("m2"))

	err := publishTestEvent(t, m, "e1")
	require.ErrorIs(t, err, errOutboxFull)
	assert.Equal(t, 2, m.outbox.len())
	assert.False(t, m.outbox.hasEvents())
}

func TestOutbox_MessageTakesThePlaceOfAnEventFirst(t *testing.T) {
	o := newOutbox(2, 1)
	subject := "flux.msg.r.g.out"
	oldest := testMsg("oldest")
	o.park(subject, oldest)
	require.True(t, o.parkEvent("flux.evt.conn.r.g", &outboxEvent{data: []byte("e1")}))

	o.park(subject, testMsg("newest"))

	assert.Equal(t, 2, o.len())
	assert.False(t, o.hasEvents(), "the event must leave first")
	assert.True(t, o.holds(outboxItem{subject: subject, msg: oldest}), "the oldest message must stay")
}

func TestOutbox_TwoEventsAreDifferentItems(t *testing.T) {
	o := newOutbox(10, 1)
	first, second := &outboxEvent{data: []byte("1")}, &outboxEvent{data: []byte("2")}
	require.True(t, o.parkEvent("s", first))
	require.True(t, o.parkEvent("s", second))

	o.remove(outboxItem{subject: "s", event: first}, false)

	assert.False(t, o.holds(outboxItem{subject: "s", event: first}))
	assert.True(t, o.holds(outboxItem{subject: "s", event: second}))
}

func TestShutdown_EndsTheRetryLoop(t *testing.T) {
	b := &eventBus{MockBus: bus.NewMockBus()}
	b.down.Store(true)
	m := newOutboxManager(t, b)
	require.NoError(t, publishTestEvent(t, m, "e1"))
	require.True(t, m.eventRetrying.Load())

	m.Shutdown()
	require.Eventually(t, func() bool { return !m.eventRetrying.Load() }, 5*time.Second, 10*time.Millisecond)
}

func TestGearRack_ResolvesAsAWireSourceDoes(t *testing.T) {
	g := &GearContextImpl{rackName: "here", gearDeploy: map[string]string{"pinned": "there"}}
	assert.Equal(t, "there", g.GearRack("pinned"))
	assert.Equal(t, "here", g.GearRack("everywhere"), "a gear with no deploy target runs on this Rack too")
}
