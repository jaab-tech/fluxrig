// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// freePort reserves a TCP port and releases it, so nothing listens on it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func watcherOpts() bus.ConnectOptions {
	return bus.ConnectOptions{Name: "watcher-test", ConnectTimeout: time.Second}
}

// The watcher stays quiet while the bus is down and signals once when it comes up.
func TestWatchForBus_SignalsWhenTheBusAppears(t *testing.T) {
	port := freePort(t)
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	restart := make(chan struct{}, 1)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		watchForBus(ctx, url, watcherOpts(), "flux", 20*time.Millisecond, restart, slog.Default())
	}()

	// Several probes fail first: nothing may be signalled while nothing listens.
	select {
	case <-restart:
		t.Fatal("signalled while the bus was down")
	case <-time.After(200 * time.Millisecond):
	}

	srv, err := snake.NewServer(context.Background(), snake.Config{
		Port:        port,
		ClusterName: "watcher-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer srv.Shutdown()

	select {
	case <-restart:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not signal after the bus came up")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher kept running after signalling")
	}
}

// Cancelling the session context stops the watcher without a signal.
func TestWatchForBus_StopsOnCancel(t *testing.T) {
	url := fmt.Sprintf("nats://127.0.0.1:%d", freePort(t))

	restart := make(chan struct{}, 1)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		watchForBus(ctx, url, watcherOpts(), "flux", 10*time.Millisecond, restart, slog.Default())
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop after cancel")
	}
	assert.Empty(t, restart)
}
