// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func testGen(t *testing.T) *idgen.IDGenerator {
	t.Helper()
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

// keepAliveOn reports whether SO_KEEPALIVE is set on the socket under conn.
func keepAliveOn(t *testing.T, conn net.Conn) bool {
	t.Helper()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var val int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		val, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE)
	}); err != nil {
		t.Fatal(err)
	}
	if gerr != nil {
		t.Fatal(gerr)
	}
	return val != 0
}

// tcpPair returns both ends of a loopback TCP connection.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	acc := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		acc <- c
	}()
	client, err = net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-acc
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func TestKeepAlive_ConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{"mode": "client", "connect": "x:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.TCPKeepAlive || time.Duration(cfg.TCPKeepAlivePeriod) != DefaultTCPKeepAlivePeriod {
		t.Errorf("defaults = %v / %v, want true / %v", cfg.TCPKeepAlive, time.Duration(cfg.TCPKeepAlivePeriod), DefaultTCPKeepAlivePeriod)
	}

	// An explicit zero period selects the default; a custom one is kept.
	cfg, _ = ParseConfig(map[string]any{"mode": "client", "connect": "x:1", "tcp_keepalive_period": "0s"})
	_ = cfg.Validate()
	if time.Duration(cfg.TCPKeepAlivePeriod) != DefaultTCPKeepAlivePeriod {
		t.Errorf("0s period = %v, want default", time.Duration(cfg.TCPKeepAlivePeriod))
	}
	cfg, _ = ParseConfig(map[string]any{"mode": "client", "connect": "x:1", "tcp_keepalive": false, "tcp_keepalive_period": "7s"})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.TCPKeepAlive || time.Duration(cfg.TCPKeepAlivePeriod) != 7*time.Second {
		t.Errorf("custom = %v / %v", cfg.TCPKeepAlive, time.Duration(cfg.TCPKeepAlivePeriod))
	}

	cfg = &Config{Mode: ModeClient, Connect: "x:1", TCPKeepAlivePeriod: Duration(-time.Second)}
	if err := cfg.Validate(); err == nil {
		t.Error("negative tcp_keepalive_period must be rejected")
	}
}

func TestKeepAlive_Apply(t *testing.T) {
	a, b := tcpPair(t)

	on := DefaultConfig()
	if err := on.applyKeepAlive(a); err != nil {
		t.Fatal(err)
	}
	if !keepAliveOn(t, a) {
		t.Error("SO_KEEPALIVE not set with tcp_keepalive on")
	}

	off := DefaultConfig()
	off.TCPKeepAlive = false
	if err := off.applyKeepAlive(b); err != nil {
		t.Fatal(err)
	}
	if keepAliveOn(t, b) {
		t.Error("SO_KEEPALIVE set with tcp_keepalive off")
	}
}

func startKeepAliveServer(t *testing.T, mutate func(*Config)) (*Server, *atomic.Int64) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Bind = "127.0.0.1:0"
	cfg.HeuristicValidation = false
	if mutate != nil {
		mutate(&cfg)
	}
	var got atomic.Int64
	s := NewServer(&cfg, testLogger(), func(*fluxmsg.FluxMsg) { got.Add(1) }, testGen(t))
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s, &got
}

func serverConnCount(s *Server) int {
	n := 0
	s.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

func TestKeepAlive_ServerAcceptedSocket(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		s, _ := startKeepAliveServer(t, func(c *Config) { c.TCPKeepAlive = enabled })
		c, err := net.Dial("tcp", s.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		waitForKeepAlive(t, "server conn", func() bool { return serverConnCount(s) == 1 })
		s.conns.Range(func(_, v any) bool {
			if got := keepAliveOn(t, v.(*Connection).conn); got != enabled {
				t.Errorf("tcp_keepalive=%v: SO_KEEPALIVE on accepted socket = %v", enabled, got)
			}
			return true
		})
		_ = c.Close()
	}
}

// writeFrame sends one length-prefixed frame (2-byte big-endian prefix).
func writeFrame(c net.Conn, payload string) error {
	frame := append([]byte{byte(len(payload) >> 8), byte(len(payload))}, payload...)
	_, err := c.Write(frame)
	return err
}

// An inbound frame restarts the idle timer: a connection that receives a frame
// more often than idle_timeout stays open past idle_timeout in total, and is
// closed once the frames stop.
func TestIdleTimeout_InboundFrameResetsTimer_Server(t *testing.T) {
	const idle = 400 * time.Millisecond
	s, got := startKeepAliveServer(t, func(c *Config) { c.IdleTimeout = Duration(idle) })

	c, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	waitForKeepAlive(t, "server conn", func() bool { return serverConnCount(s) == 1 })

	// Keep the link busy for three idle periods.
	start := time.Now()
	for time.Since(start) < 3*idle {
		if err := writeFrame(c, "0800000000000000"); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(idle / 4)
	}
	if n := serverConnCount(s); n != 1 {
		t.Fatalf("connection closed while frames were arriving (conns=%d)", n)
	}
	if got.Load() == 0 {
		t.Fatal("no frames emitted")
	}

	// Silence: now idle_timeout must close it.
	waitForKeepAlive(t, "idle close", func() bool { return serverConnCount(s) == 0 })
}

func TestIdleTimeout_InboundFrameResetsTimer_Client(t *testing.T) {
	const idle = 400 * time.Millisecond
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	cfg := DefaultConfig()
	cfg.Mode = ModeClient
	cfg.Connect = l.Addr().String()
	cfg.HeuristicValidation = false
	cfg.IdleTimeout = Duration(idle)
	cfg.ReconnectWait = Duration(time.Hour) // no redial inside the test

	var got, up, down atomic.Int64
	cl := NewClient(&cfg, testLogger(), func(*fluxmsg.FluxMsg) { got.Add(1) }, testGen(t))
	cl.OnLinkState(func(isUp bool, _ string) {
		if isUp {
			up.Add(1)
		} else {
			down.Add(1)
		}
	})
	if err = cl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Stop() }()

	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	waitForKeepAlive(t, "client up", func() bool { return up.Load() == 1 })
	if !keepAliveOn(t, cl.connPtr.Load().conn) {
		t.Error("SO_KEEPALIVE not set on the dialed socket")
	}

	start := time.Now()
	for time.Since(start) < 3*idle {
		if err := writeFrame(peer, "0810000000000000"); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(idle / 4)
	}
	if down.Load() != 0 {
		t.Fatal("client dropped the link while frames were arriving")
	}
	if got.Load() == 0 {
		t.Fatal("no frames emitted")
	}

	waitForKeepAlive(t, "idle close", func() bool { return down.Load() == 1 })
}

// waitForKeepAlive polls cond until it holds or the deadline passes.
func waitForKeepAlive(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
