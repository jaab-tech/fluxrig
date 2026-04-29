// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func TestNATSControlPlane(t *testing.T) {
	// 1. Start Snake (ephemeral NATS)
	s, err := snake.NewServer(snake.Config{
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

func TestNATSControlPlane_Error(t *testing.T) {
	// Start Snake (ephemeral NATS)
	s, err := snake.NewServer(snake.Config{
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
