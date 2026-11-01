// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
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
	start := time.Now()
	if err := srv.Drain(ctx); err != nil {
		t.Fatalf("Drain with an idle connection: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Drain took %s with only idle connections", elapsed)
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
// Drain waits for it, and gives up at its deadline with an error that names how
// many connections still read.
func TestServerDrainWaitsForAHandlerInsideEmit(t *testing.T) {
	srv, release := stuckServer(t, time.Second)
	time.AfterFunc(150*time.Millisecond, func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("Drain returned after %s, before the handler left emit", elapsed)
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
