// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func lateConn(t *testing.T) *nats.Conn {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "late-test", StoreDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func receive(t *testing.T, ch <-chan Command) Command {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no command arrived")
		return Command{}
	}
}

// The reason it exists: a gear subscribes before there is a connection, and the
// commands reach it after one is bound.
func TestLateControlPlane_SubscribeBeforeTheConnection(t *testing.T) {
	cp := NewLateControlPlane()
	ch, err := cp.Subscribe("sim-1")
	require.NoError(t, err, "a gear can subscribe with no bus")
	assert.False(t, cp.Bound())

	nc := lateConn(t)
	cp.Bind(nc)
	assert.True(t, cp.Bound())

	require.NoError(t, NewNATSControlPlane(nc).Publish("sim-1", Command{Cmd: CmdSimStart, Src: "mixer"}))
	got := receive(t, ch)
	assert.Equal(t, CmdSimStart, got.Cmd)
	assert.Equal(t, "mixer", got.Src)
}

func TestLateControlPlane_SubscribeAfterTheConnection(t *testing.T) {
	cp := NewLateControlPlane()
	nc := lateConn(t)
	cp.Bind(nc)

	ch, err := cp.Subscribe("sim-2")
	require.NoError(t, err)
	require.NoError(t, NewNATSControlPlane(nc).Publish("sim-2", Command{Cmd: CmdSimStop}))

	assert.Equal(t, CmdSimStop, receive(t, ch).Cmd)
}

func TestLateControlPlane_PublishToNobodyWithoutAConnectionFails(t *testing.T) {
	cp := NewLateControlPlane()

	err := cp.Publish("x", Command{Cmd: "any"})
	assert.ErrorIs(t, err, ErrControlPlaneOffline)

	cp.Bind(lateConn(t))
	require.NoError(t, cp.Publish("x", Command{Cmd: "any"}), "with a connection it goes to the bus")
}

// Without a bus the gears of one Rack still signal each other: the ISO client gear
// reports a link going down and the Conductor that senses it hears it.
func TestLateControlPlane_WithoutABusCommandsStayInsideTheRack(t *testing.T) {
	cp := NewLateControlPlane()
	ch, err := cp.Subscribe("link.uplink-a")
	require.NoError(t, err)
	other, _ := cp.Subscribe("link.uplink-b")

	require.NoError(t, cp.Publish("link.uplink-a", Command{Cmd: "conn.down", Src: "uplink-a"}))

	got := receive(t, ch)
	assert.Equal(t, "conn.down", got.Cmd)
	select {
	case stray := <-other:
		t.Fatalf("delivered to another gear: %+v", stray)
	case <-time.After(100 * time.Millisecond):
	}
}

// Once bound, a command is delivered once, over the bus, and not also in memory.
func TestLateControlPlane_WithABusACommandIsDeliveredOnce(t *testing.T) {
	cp := NewLateControlPlane()
	ch, _ := cp.Subscribe("g")
	nc := lateConn(t)
	cp.Bind(nc)

	require.NoError(t, cp.Publish("g", Command{Cmd: "once"}))

	assert.Equal(t, "once", receive(t, ch).Cmd)
	select {
	case extra := <-ch:
		t.Fatalf("delivered twice: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// Binding the same connection twice must not deliver a command twice.
func TestLateControlPlane_BindingTwiceDoesNotDuplicate(t *testing.T) {
	cp := NewLateControlPlane()
	ch, _ := cp.Subscribe("g")
	nc := lateConn(t)

	cp.Bind(nc)
	cp.Bind(nc)
	cp.Bind(nil) // ignored
	require.NoError(t, NewNATSControlPlane(nc).Publish("g", Command{Cmd: "once"}))
	require.NoError(t, nc.Flush())

	assert.Equal(t, "once", receive(t, ch).Cmd)
	select {
	case extra := <-ch:
		t.Fatalf("delivered twice: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// A gear that has stopped must not keep listening: a scenario applied again starts the
// gear anew, and the subscription of the one before it would stay, deliver into a
// channel nobody reads, and be attached again on every rejoin.
func TestLateControlPlane_ReleaseEndsTheSubscriptionsOfAGear(t *testing.T) {
	cp := NewLateControlPlane()
	nc := lateConn(t)
	cp.Bind(nc)

	old, err := cp.Subscribe("g")
	require.NoError(t, err)
	other, err := cp.Subscribe("other")
	require.NoError(t, err)
	cp.Release("g")
	cp.Release("g")      // safe to repeat
	cp.Release("nobody") // safe for a gear with none

	fresh, err := cp.Subscribe("g")
	require.NoError(t, err)
	require.NoError(t, cp.Publish("g", Command{Cmd: "new"}))
	require.NoError(t, cp.Publish("other", Command{Cmd: "kept"}))
	require.NoError(t, nc.Flush())

	assert.Equal(t, "new", receive(t, fresh).Cmd)
	assert.Equal(t, "kept", receive(t, other).Cmd, "releasing one gear must leave the others")
	select {
	case c := <-old:
		t.Fatalf("the released subscription still received %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	assert.Len(t, cp.subs, 2, "only the live subscriptions are kept")
}

// Without a connection a released gear is forgotten as well, so a command for it is
// reported as undeliverable and not accepted by a listener that has gone.
func TestLateControlPlane_ReleaseForgetsAnUnattachedSubscription(t *testing.T) {
	cp := NewLateControlPlane()
	_, err := cp.Subscribe("g")
	require.NoError(t, err)
	cp.Release("g")

	assert.ErrorIs(t, cp.Publish("g", Command{Cmd: "x"}), ErrControlPlaneOffline)
}

// A gear that stopped reading must not hold up its subscription: the commands past the
// queue are dropped, so that nothing waits in the server for a reader that is gone.
func TestLateControlPlane_AFullQueueDropsInsteadOfBlocking(t *testing.T) {
	cp := NewLateControlPlane()
	nc := lateConn(t)
	cp.Bind(nc)
	ch, err := cp.Subscribe("g") // never read
	require.NoError(t, err)

	for i := 0; i < 250; i++ {
		require.NoError(t, cp.Publish("g", Command{Cmd: "flood"}))
	}
	require.NoError(t, nc.Flush())

	cp.mu.Lock()
	sub := cp.subs[0].sub
	cp.mu.Unlock()
	require.Eventually(t, func() bool {
		pending, _, err := sub.Pending()
		return err == nil && pending == 0 && len(ch) == cap(ch)
	}, 5*time.Second, 10*time.Millisecond,
		"a blocked delivery leaves the commands past the queue waiting in the subscription")
}

// A subscription that cannot be made with a connection bound is an error the gear
// hears, and one that failed to attach is tried again at the next Bind.
func TestLateControlPlane_SubscribeReportsAFailureAndBindRetries(t *testing.T) {
	cp := NewLateControlPlane()
	nc := lateConn(t)
	cp.Bind(nc)
	nc.Close()

	_, err := cp.Subscribe("g")
	require.Error(t, err, "a closed connection cannot take a subscription")

	// A subscription made before the connection whose attach fails is kept for a retry.
	cp2 := NewLateControlPlane()
	ch, err := cp2.Subscribe("late")
	require.NoError(t, err)
	dead, err := nats.Connect(lateConn(t).ConnectedUrl())
	require.NoError(t, err)
	dead.Close()
	cp2.Bind(dead) // the attach fails: the connection is closed
	cp2.mu.Lock()
	require.Nil(t, cp2.subs[0].sub, "the failed attach left it unattached")
	cp2.mu.Unlock()

	good := lateConn(t)
	cp2.Bind(good) // a different, working connection: attached now
	require.NoError(t, cp2.Publish("late", Command{Cmd: "retry"}))
	require.NoError(t, good.Flush())
	assert.Equal(t, "retry", receive(t, ch).Cmd)
}
