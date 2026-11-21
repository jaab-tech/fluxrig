// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// drainServer is a server with one idle client connection, as a terminal
// that is connected and quiet.
func drainServer(t *testing.T) (*Server, net.Conn) {
	t.Helper()
	srv, _ := newMissingConnServer(t, OnMissingConnectionDrop)
	c := dialNew(t, srv, 0)
	return srv, c
}

// Drain stops the server from reading and accepting, keeps the sockets open
// for replies, and returns. It does not wait for the connections to close:
// they close at Stop, which comes after Drain. Before, every connection waited
// for Stop and Drain waited for every connection, so a Drain with one open
// connection ran to its deadline.
func TestServerDrainReturnsWithIdleConnections(t *testing.T) {
	srv, _ := drainServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Drain(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain with an idle connection: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Drain did not return before its deadline with only idle connections")
	}
	// Drain waits for readers, not for connections: no connection is
	// reading, and the idle one is still the server's to close at Stop.
	if got := srv.readers.Load(); got != 0 {
		t.Fatalf("Drain returned with %d connection(s) still reading", got)
	}
	if got := srv.activeConns.Load(); got == 0 {
		t.Fatal("Drain closed the idle connection; only Stop closes connections")
	}
}

// After Drain the socket stays open for writing: a reply that is still on its
// way is delivered. Stop then closes it.
func TestServerDrainKeepsTheSocketOpenForReplies(t *testing.T) {
	srv, sink := newMissingConnServer(t, OnMissingConnectionDrop)
	c, err := net.Dial("tcp", srv.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err = c.Write(frameWith(nil)); err != nil {
		t.Fatal(err)
	}
	connID := sink.first(t).Metadata["conn.id"]

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = srv.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if _, err = srv.Process(context.Background(), replyFor(connID, "127.0.0.1")); err != nil {
		t.Fatalf("a reply after Drain must still be written: %v", err)
	}
	if !receives(t, c, 2*time.Second) {
		t.Fatal("the reply written after Drain did not reach the client")
	}

	if err = srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err = c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("after Stop the connection must be closed, read gave %v", err)
	}
}

// A handler that is still inside the emit of a frame has not stopped reading:
// Drain waits for it, returning only after the handler leaves emit. The
// ordering is what the test asserts: Drain is still waiting while the handler
// is inside, and returns once it leaves.
func TestServerDrainWaitsForAHandlerInsideEmit(t *testing.T) {
	srv, release := stuckServer(t, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Drain(ctx) }()

	// While the handler is inside emit, Drain must still be waiting: a return
	// here, for any reason, is the failure. The grace only gives a wrong
	// return a chance to show itself; it asserts nothing about Drain's pace.
	select {
	case err := <-done:
		t.Fatalf("Drain returned %v before the handler left emit", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Drain did not return after the handler left emit")
	}
}

func TestServerDrainGivesUpWhileAHandlerReads(t *testing.T) {
	srv, release := stuckServer(t, 0)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := srv.Drain(ctx)
	if err == nil || !strings.Contains(err.Error(), "still reading") {
		t.Fatalf("Drain = %v, want an error that says how many connections still read", err)
	}
}

// A second Stop is harmless: the gear and its owner can both stop the server.
func TestServerStopTwice(t *testing.T) {
	srv, _ := drainServer(t)
	if err := srv.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// errorCounter counts the records at ERROR that a server logs.
type errorCounter struct {
	slog.Handler
	mu   sync.Mutex
	msgs []string
}

func (h *errorCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h *errorCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.msgs = append(h.msgs, r.Message)
	}
	return nil
}
func (h *errorCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *errorCounter) WithGroup(string) slog.Handler      { return h }
func (h *errorCounter) errors() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.msgs...)
}

// Drain closes the listener on purpose, so a graceful stop has no accept error
// to report. Before, the accept loop logged one at ERROR for every retry between
// Drain and Stop, on every graceful stop.
func TestServerDrainLogsNoAcceptError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeServer
	cfg.Bind = "127.0.0.1:0"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	logs := &errorCounter{}
	srv := NewServer(&cfg, slog.New(logs), (&tpduSink{}).emit, gen)
	if err = srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = srv.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.acceptDone:
	case <-ctx.Done():
		t.Fatalf("the accept loop still runs after Drain: %q", logs.errors())
	}
	if got := logs.errors(); len(got) != 0 {
		t.Fatalf("a graceful stop logged at ERROR: %q", got)
	}
}
