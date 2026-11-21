// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/internal/natstest"
)

// freezer forwards TCP to target until it is frozen: then it keeps every
// connection open and passes nothing, as a server that crashed, or a cut
// link, looks from the client.
type freezer struct {
	ln     net.Listener
	target string
	frozen atomic.Bool
}

func newFreezer(t *testing.T, target string) *freezer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &freezer{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *freezer) serve(c net.Conn) {
	s, err := net.Dial("tcp", f.target)
	if err != nil {
		_ = c.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { _ = c.Close(); _ = s.Close() }
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if err != nil {
				once.Do(closeBoth)
				return
			}
			if f.frozen.Load() {
				continue // swallowed: the peer hears nothing, and no close
			}
			if _, err = dst.Write(buf[:n]); err != nil {
				once.Do(closeBoth)
				return
			}
		}
	}
	go pipe(s, c)
	pipe(c, s)
}

// A server that goes silent without closing the connection is left after
// the client's pings run out, for another server of the list: the client
// notices it in about PingInterval times MaxPingsOut, not in the minutes
// the client defaults take.
func TestConnect_LeavesASilentServer(t *testing.T) {
	servers := natstest.Cluster(t, 2)
	silent := newFreezer(t, servers[0].Addr().String())
	nb := NewNatsBus("flux-msg")
	// Only the silent server in the list: the client learns the others from
	// the cluster, and a list of two would be tried in random order.
	silentURL := "nats://" + silent.ln.Addr().String()
	require.NoError(t, nb.Connect(silentURL, ConnectOptions{
		Name: t.Name(), ConnectTimeout: 2 * time.Second, ReconnectWait: 100 * time.Millisecond,
		PingInterval: 200 * time.Millisecond, MaxPingsOut: 2,
	}))
	t.Cleanup(nb.Close)
	nc := nb.Core().(interface {
		ConnectedUrl() string
		IsConnected() bool
	})
	require.Equal(t, silentURL, nc.ConnectedUrl())

	silent.frozen.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for !(nc.IsConnected() && nc.ConnectedUrl() != silentURL) {
		require.True(t, time.Now().Before(deadline), "still on the silent server after 10s: connected=%v url=%q", nc.IsConnected(), nc.ConnectedUrl())
		time.Sleep(50 * time.Millisecond) // poll interval
	}
}
