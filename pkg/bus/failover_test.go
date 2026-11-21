// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// runFailoverServer starts a standalone server. With jetStream it also holds
// the stream the bus publishes to.
func runFailoverServer(t *testing.T, jetStream bool) *natsserver.Server {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true}
	if jetStream {
		opts.JetStream = true
		opts.StoreDir = t.TempDir()
	}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "server did not start")
	t.Cleanup(srv.Shutdown)
	if jetStream {
		nc, err := nats.Connect(srv.ClientURL())
		require.NoError(t, err)
		defer nc.Close()
		js, err := nc.JetStream()
		require.NoError(t, err)
		_, err = js.AddStream(&nats.StreamConfig{Name: "failover", Subjects: []string{"failover.>"}})
		require.NoError(t, err)
	}
	return srv
}

// A server that holds the connection but reaches no stream is what a Rack
// sees through a Mixer cut off from its cluster. After FailoverAfter such
// publishes in a row the bus moves to the next server in its URL list.
func TestNatsBus_FailsOverWhenNoStreamAnswers(t *testing.T) {
	dead := runFailoverServer(t, false) // no JetStream: every publish finds no stream
	live := runFailoverServer(t, true)
	url := dead.ClientURL() + "," + live.ClientURL()
	// The bus checks JetStream for twice the operation timeout before it
	// leaves, to wait out an election: 2s here.
	opts := ConnectOptions{ConnectTimeout: 2 * time.Second, ReconnectWait: 50 * time.Millisecond, FailoverAfter: 2, OperationTimeout: time.Second}

	// The client shuffles the URL list, so connect until it lands on the
	// dead server first. Each try is 50/50.
	var b *NatsBus
	for i := 0; i < 50 && b == nil; i++ {
		c := NewNatsBus("failover")
		require.NoError(t, c.Connect(url, opts))
		if c.conn.ConnectedUrl() == dead.ClientURL() {
			b = c
		} else {
			c.Close()
		}
	}
	require.NotNil(t, b, "never connected to the dead server first")
	defer b.Close()

	publish := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return b.PublishRaw(ctx, "failover.x", []byte("x"), uuid.New())
	}
	require.Error(t, publish(), "first publish through the dead server")
	require.Equal(t, dead.ClientURL(), b.conn.ConnectedUrl(), "one failure must not move the bus")
	require.Error(t, publish(), "second publish through the dead server")

	deadline := time.Now().Add(10 * time.Second)
	for b.conn.ConnectedUrl() != live.ClientURL() || !b.conn.IsConnected() {
		require.True(t, time.Now().Before(deadline), "bus did not move to the live server, on %q", b.conn.ConnectedUrl())
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	require.NoError(t, publish(), "publish after the failover")
}

// With FailoverAfter zero the bus stays where it is, as it did before.
func TestNatsBus_NoFailoverWhenDisabled(t *testing.T) {
	dead := runFailoverServer(t, false)
	b := NewNatsBus("failover")
	require.NoError(t, b.Connect(dead.ClientURL(), ConnectOptions{ConnectTimeout: 2 * time.Second}))
	defer b.Close()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		require.Error(t, b.PublishRaw(ctx, "failover.x", []byte("x"), uuid.New()))
		cancel()
	}
	require.Zero(t, b.pubMisses.Load(), "a disabled failover counts nothing")
	require.True(t, b.conn.IsConnected())
}

// A Rack that only subscribes publishes too seldom to notice a server cut
// off from its cluster. Its consumer requests count toward the failover
// too: after FailoverAfter of them find no JetStream, the bus moves, and
// the subscription is made on the live server.
func TestNatsBus_FailsOverOnConsumerRequests(t *testing.T) {
	dead := runFailoverServer(t, false)
	live := runFailoverServer(t, true)
	url := dead.ClientURL() + "," + live.ClientURL()
	opts := ConnectOptions{ConnectTimeout: 2 * time.Second, ReconnectWait: 50 * time.Millisecond, FailoverAfter: 2,
		OperationTimeout: time.Second, SubscriptionRetryAttempts: 60, SubscriptionRetryWait: 100 * time.Millisecond}
	var b *NatsBus
	for i := 0; i < 50 && b == nil; i++ {
		c := NewNatsBus("failover")
		require.NoError(t, c.Connect(url, opts))
		if c.conn.ConnectedUrl() == dead.ClientURL() {
			b = c
		} else {
			c.Close()
		}
	}
	require.NotNil(t, b, "never connected to the dead server first")
	defer b.Close()

	sub, err := b.Subscribe("failover.sub", func(context.Context, *fluxmsg.FluxMsg) error { return nil })
	require.NoError(t, err, "the subscription is made once the bus has moved")
	defer func() { _ = sub.Unsubscribe() }()
	require.Equal(t, live.ClientURL(), b.conn.ConnectedUrl(), "consumer requests alone moved the bus")
}

// A server that reaches JetStream while one stream answers no publish is
// what every server looks like while that stream elects a leader after a
// NATS server died. The bus stays: leaving cost a Rack a minute without a
// connection across a slow link, and its events arrived too old.
func TestNatsBus_StaysWhenTheServerReachesJetStream(t *testing.T) {
	srv := runFailoverServer(t, true)
	other := runFailoverServer(t, true) // where the bus would go, were it to leave
	url := srv.ClientURL() + "," + other.ClientURL()
	opts := ConnectOptions{ConnectTimeout: 2 * time.Second, ReconnectWait: 50 * time.Millisecond, FailoverAfter: 2, OperationTimeout: time.Second}
	// The client shuffles the URL list: connect until it lands on srv.
	var b *NatsBus
	for i := 0; i < 50 && b == nil; i++ {
		c := NewNatsBus("failover")
		require.NoError(t, c.Connect(url, opts))
		if c.conn.ConnectedUrl() == srv.ClientURL() {
			b = c
		} else {
			c.Close()
		}
	}
	require.NotNil(t, b, "never connected to srv first")
	defer b.Close()

	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		require.Error(t, b.PublishRaw(ctx, "nostream.x", []byte("x"), uuid.New()), "no stream holds this subject")
		cancel()
	}
	// The check runs beside the publishes: wait until it ran.
	deadline := time.Now().Add(5 * time.Second)
	for b.probing.Load() {
		require.True(t, time.Now().Before(deadline), "the JetStream check did not end")
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	require.True(t, b.conn.IsConnected())
	require.Zero(t, b.conn.Stats().Reconnects, "the bus left a server that reaches JetStream")
	require.Equal(t, srv.ClientURL(), b.conn.ConnectedUrl())
}

// The error a Rack got when a failover reconnect closed its connection under
// the creation of its enrollment consumer, which ended the Rack process.
func TestDisconnected(t *testing.T) {
	lost := fmt.Errorf("failed to create ephemeral consumer for x: %w",
		fmt.Errorf("bus: replica count of the consumer for x: %w",
			fmt.Errorf("bus: read stream flux-msg: %w", nats.ErrConnectionClosed)))
	require.True(t, Disconnected(lost))
	require.True(t, Disconnected(nats.ErrConnectionReconnecting))
	require.True(t, Disconnected(nats.ErrDisconnected))
	require.False(t, Disconnected(nats.ErrBadSubject), "an error of the request itself is not a lost connection")
}

// While the cluster elects its metadata leader a healthy server cannot
// answer the JetStream account info for some seconds. A Rack that asked
// once then left a healthy server, and reconnecting across a slow link
// cost it its events. A server that answers within the window is kept.
func TestNatsBus_StaysWhenJetStreamAnswersWithinTheWindow(t *testing.T) {
	srv := runFailoverServer(t, false) // no JetStream yet: the election
	other := runFailoverServer(t, true)
	url := srv.ClientURL() + "," + other.ClientURL()
	opts := ConnectOptions{ConnectTimeout: 2 * time.Second, ReconnectWait: 50 * time.Millisecond, FailoverAfter: 2, OperationTimeout: 2 * time.Second}
	var b *NatsBus
	for i := 0; i < 50 && b == nil; i++ {
		c := NewNatsBus("failover")
		require.NoError(t, c.Connect(url, opts))
		if c.conn.ConnectedUrl() == srv.ClientURL() {
			b = c
		} else {
			c.Close()
		}
	}
	require.NotNil(t, b, "never connected to srv first")
	defer b.Close()

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		require.Error(t, b.PublishRaw(ctx, "nostream.x", []byte("x"), uuid.New()))
		cancel()
	}
	// The election ends inside the window (2 x 2s).
	require.NoError(t, srv.EnableJetStream(&natsserver.JetStreamConfig{StoreDir: t.TempDir()}))

	deadline := time.Now().Add(10 * time.Second)
	for b.probing.Load() {
		require.True(t, time.Now().Before(deadline), "the JetStream check did not end")
		time.Sleep(20 * time.Millisecond) // poll interval
	}
	require.Zero(t, b.conn.Stats().Reconnects, "the bus left a server whose JetStream answered within the window")
}
