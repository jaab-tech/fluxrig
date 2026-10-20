// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// startOutboxServer boots an embedded server with a message stream and returns
// it with a connected bus. Each call uses its own store dir and port.
func startOutboxServer(t *testing.T, storeDir string) (*snake.Server, *bus.NatsBus) {
	t.Helper()
	srv, err := snake.NewServer(context.Background(), snake.Config{
		Port:           -1,
		ClusterName:    t.Name(),
		StoreDir:       storeDir,
		StreamName:     "flux-msg",
		StreamSubjects: []string{"flux.msg.>"},
	})
	require.NoError(t, err)

	nb := bus.NewNatsBus("flux-msg")
	require.NoError(t, nb.Connect(srv.ClientURL(), bus.ConnectOptions{
		Name:           t.Name(),
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  200 * time.Millisecond,
	}))
	return srv, nb
}

// An emission made while the real bus is down is parked, and a SetBus with a
// reconnected bus forwards it. This is the integration shape of the claim: a
// real publish failure, a real rejoin, a real arrival. On the old code the
// emit returned the error and nothing was ever parked.
func TestOutbox_RealBusDownAndRejoin(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()

	srv, nb := startOutboxServer(t, storeDir)

	gen, err := idgen.New(uuid.New())
	require.NoError(t, err)
	mgr := NewManager(uuid.New(), "rack-a", nb, gen, &MockManager{}, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)
	require.NoError(t, mgr.SetOutboxConfig(100, 2, ""))
	defer mgr.Shutdown()

	subject := "flux.msg.rack-a.g.out"
	busSubject(mgr, subject)

	// Sanity: with the bus up, the emission goes straight through.
	require.NoError(t, mgr.publishPort(ctx, "g", "out", testMsg("live")))
	require.Equal(t, 0, mgr.outbox.len(), "nothing is parked while the bus answers")

	// Kill the server. The client reconnects in the background; publishes fail
	// once the operation gives up on the dead connection.
	srv.Shutdown()
	nb.Close()

	// The failure may take a moment to surface through reconnect buffering;
	// poll until the emission parks instead of asserting on the first try.
	parked := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if perr := mgr.publishPort(ctx, "g", "out", testMsg("parked-1")); perr == nil {
			if mgr.outbox.len() > 0 {
				parked = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, parked, "an emission while the bus is down must park, not fail")
	require.NoError(t, mgr.publishPort(ctx, "g", "out", testMsg("parked-2")))
	require.Equal(t, 2, mgr.outbox.len())

	// Restart the server on the same store and rejoin with a fresh bus.
	srv2, nb2 := startOutboxServer(t, storeDir)
	defer srv2.Shutdown()
	defer nb2.Close()

	// Subscribe before the drain so arrival itself is asserted, not just that
	// the outbox emptied.
	type arrival struct {
		subject string
		v       string
	}
	arrived := make(chan arrival, 8)
	sub, err := nb2.Subscribe(subject, func(ctx context.Context, m *fluxmsg.FluxMsg) error {
		arrived <- arrival{subject: subject, v: m.Data["v"].(string)}
		return nil
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	mgr.SetBus(nb2)

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.outbox.len() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, 0, mgr.outbox.len(), "the rejoin must drain everything parked")

	var got []string
	timeout := time.After(10 * time.Second)
	for len(got) < 2 {
		select {
		case a := <-arrived:
			got = append(got, a.v)
		case <-timeout:
			t.Fatalf("only %d of 2 parked messages arrived after rejoin: %v", len(got), got)
		}
	}
	require.Equal(t, []string{"parked-1", "parked-2"}, got, "parked messages must arrive in order")
}
