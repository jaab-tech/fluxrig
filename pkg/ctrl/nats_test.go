// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func TestNATSControlPlane(t *testing.T) {
	// 1. Start Snake (ephemeral NATS)
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "ctrl-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	// 2. Connect to NATS
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	cp := NewNATSControlPlane(nc)
	require.NotNil(t, cp)

	myGearID := "gear-a"
	targetGearID := "gear-b"

	// 3. Subscribe
	ch, err := cp.Subscribe(myGearID)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// 4. Publish to self (loopback test)
	cmd := Command{
		Cmd: "test.cmd",
		Args: map[string]string{
			"foo": "bar",
		},
		Src: "tester",
	}

	err = cp.Publish(myGearID, cmd)
	require.NoError(t, err)

	// 5. Receive
	select {
	case received := <-ch:
		require.Equal(t, cmd.Cmd, received.Cmd)
		require.Equal(t, cmd.Args["foo"], received.Args["foo"])
		require.Equal(t, cmd.Src, received.Src)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for control command")
	}

	// 6. Test Publish to another gear
	chB, err := cp.Subscribe(targetGearID)
	require.NoError(t, err)

	err = cp.Publish(targetGearID, cmd)
	require.NoError(t, err)

	select {
	case received := <-chB:
		require.Equal(t, cmd.Cmd, received.Cmd)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for control command on gear B")
	}
}

// A listener that has room in its queue acknowledges receipt: this is the
// case handleSimControl's bare Publish could never distinguish from nobody
// being there at all.
func TestConfirmedPublish_AckedWhenAListenerHasRoom(t *testing.T) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "confirmed-publish-ok",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	cp := NewNATSControlPlane(nc)
	ch, err := cp.Subscribe("sim-1")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, ConfirmedPublish(ctx, nc, "sim-1", Command{Cmd: CmdSimStart}))

	select {
	case received := <-ch:
		require.Equal(t, CmdSimStart, received.Cmd)
	case <-time.After(2 * time.Second):
		t.Fatal("the gear must still receive the command, not just acknowledge it")
	}
}

// Before this fix, the Mixer's HTTP API reported success the moment NATS
// accepted a fire-and-forget publish, even with nobody subscribed to receive
// it. ConfirmedPublish must fail instead.
func TestConfirmedPublish_FailsWhenNoOneIsListening(t *testing.T) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "confirmed-publish-no-listener",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = ConfirmedPublish(ctx, nc, "nobody-home", Command{Cmd: CmdSimStart})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAck)
}

// A listener whose queue is already full drops the command (subscribeInto's
// existing behavior) and must not acknowledge it: ConfirmedPublish's caller
// needs to learn that as clearly as it would learn no listener at all.
func TestConfirmedPublish_FailsWhenTheListenersQueueIsFull(t *testing.T) {
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "confirmed-publish-full-queue",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	cp := NewNATSControlPlane(nc)
	ch, err := cp.Subscribe("sim-full")
	require.NoError(t, err)

	// Fill the 100-slot queue without draining it.
	for i := 0; i < 100; i++ {
		require.NoError(t, cp.Publish("sim-full", Command{Cmd: CmdSimRate}))
	}
	require.Eventually(t, func() bool { return len(ch) == 100 }, 2*time.Second, 10*time.Millisecond,
		"the queue must actually be full before the confirmed publish is tried")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = ConfirmedPublish(ctx, nc, "sim-full", Command{Cmd: CmdSimStart})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAck)
}

func TestNATSControlPlane_Error(t *testing.T) {
	// Start Snake (ephemeral NATS)
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: "ctrl-error-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	// Connect and then close to test error paths
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	nc.Close()

	cp := NewNATSControlPlane(nc)

	err = cp.Publish("any", Command{})
	require.Error(t, err)

	_, err = cp.Subscribe("any")
	require.Error(t, err)
}
