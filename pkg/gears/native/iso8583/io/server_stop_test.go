// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// stuckServer starts a server whose emit blocks until release is closed, and
// parks one connection handler inside that emit: the handler has read a frame
// and has not finished with it. entered is closed when the handler is inside.
func stuckServer(t *testing.T, stop time.Duration) (srv *Server, release chan struct{}) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode = ModeServer
	cfg.Bind = "127.0.0.1:0"
	cfg.FrameLengthSize = 2
	cfg.StopTimeout = Duration(stop)
	cfg.ReadTimeout = Duration(2 * time.Second)
	cfg.WriteTimeout = Duration(2 * time.Second)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	release = make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	emit := func(*fluxmsg.FluxMsg) {
		once.Do(func() { close(entered) })
		<-release
	}
	srv = NewServer(&cfg, slog.New(slog.DiscardHandler), emit, gen)
	if err = srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", srv.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err = c.Write(frameWith(nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never reached emit")
	}
	return srv, release
}

// Stop returns only when the connection handlers are done: while one is still
// inside emit, Stop waits for it, and on return nothing of the server runs.
func TestServerStopWaitsForConnectionHandlers(t *testing.T) {
	srv, release := stuckServer(t, 3*time.Second)
	const hold = 150 * time.Millisecond
	time.AfterFunc(hold, func() { close(release) })

	start := time.Now()
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed < hold {
		t.Fatalf("Stop returned after %s, before the handler could finish (%s)", elapsed, hold)
	}
	if n := srv.activeConns.Load(); n != 0 {
		t.Fatalf("%d connection handler(s) still running after Stop returned", n)
	}
	if n := connCountOf(srv); n != 0 {
		t.Fatalf("%d connection(s) still in the table after Stop returned", n)
	}
}

// A handler that never finishes does not hold Stop for ever: after stop_timeout
// it returns an error that says how many are left.
func TestServerStopGivesUpAfterStopTimeout(t *testing.T) {
	const limit = 200 * time.Millisecond
	srv, release := stuckServer(t, limit)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	err := srv.Stop()
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Stop = %v, want an error that names the handlers still running", err)
	}
	if elapsed < limit || elapsed > 5*limit {
		t.Fatalf("Stop took %s, want about %s", elapsed, limit)
	}
}

// stop_timeout of 0 does not wait: the previous behaviour.
func TestServerStopTimeoutZeroDoesNotWait(t *testing.T) {
	srv, release := stuckServer(t, 0)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Stop took %s with stop_timeout 0", elapsed)
	}
}
