// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// missingConnFixture is a server with one client that has already gone.
type missingConnFixture struct {
	srv    *Server
	sink   *tpduSink
	lostID string // conn.id of the connection that the peer dropped
}

func newMissingConnServer(t *testing.T, policy string) (*Server, *tpduSink) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode = ModeServer
	cfg.Bind = "127.0.0.1:0"
	cfg.FrameLengthSize = 2
	cfg.OnMissingConnection = policy
	cfg.ReadTimeout = Duration(2 * time.Second)
	cfg.WriteTimeout = Duration(2 * time.Second)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	sink := &tpduSink{}
	srv := NewServer(&cfg, slog.New(slog.DiscardHandler), sink.emit, gen)
	if err = srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, sink
}

func connCountOf(s *Server) int {
	n := 0
	s.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

// waitConns polls until the server holds want connections.
func waitConns(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if connCountOf(s) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server holds %d connections, want %d", connCountOf(s), want)
}

// dropAPeer connects, sends one request, learns its conn.id and closes.
func dropAPeer(t *testing.T, srv *Server, sink *tpduSink) *missingConnFixture {
	t.Helper()
	c, err := net.Dial("tcp", srv.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write(frameWith(nil)); err != nil {
		t.Fatal(err)
	}
	m := sink.first(t)
	_ = c.Close()
	waitConns(t, srv, 0)
	return &missingConnFixture{srv: srv, sink: sink, lostID: m.Metadata["conn.id"]}
}

// dialNew opens a new client and waits until the server has accepted it.
func dialNew(t *testing.T, srv *Server, already int) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", srv.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	waitConns(t, srv, already+1)
	return c
}

// reply is what the conductor hands back: the connection and the peer address
// of the request that it parked.
func replyFor(lostID, peerIP string) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.RawPayload = tpduTestBody
	m.Metadata["conn.id"] = lostID
	if peerIP != "" {
		m.Metadata["peer.ip"] = peerIP
	}
	return m
}

func receives(t *testing.T, c net.Conn, wait time.Duration) bool {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, len(frameWith(nil)))
	_, err := io.ReadFull(c, buf)
	return err == nil
}

// A peer drops its link and reconnects before the reply is ready. What the
// server does with the reply depends on on_missing_connection.
func TestOnMissingConnection(t *testing.T) {
	cases := []struct {
		name     string
		policy   string
		peerIP   string
		wantSent bool
	}{
		{"drop refuses the reply", OnMissingConnectionDrop, "127.0.0.1", false},
		{"same_remote_ip sends it on the new connection of that IP", OnMissingConnectionSameIP, "127.0.0.1", true},
		{"same_remote_ip never sends it to another IP", OnMissingConnectionSameIP, "192.0.2.77", false},
		{"same_remote_ip without a peer address refuses", OnMissingConnectionSameIP, "", false},
		{"same_listener sends it on the new connection whatever the IP", OnMissingConnectionListener, "192.0.2.77", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, sink := newMissingConnServer(t, tc.policy)
			fx := dropAPeer(t, srv, sink)
			again := dialNew(t, srv, 0) // the peer reconnected

			_, err := srv.Process(context.Background(), replyFor(fx.lostID, tc.peerIP))
			if tc.wantSent {
				if err != nil {
					t.Fatalf("Process: %v", err)
				}
				if !receives(t, again, 2*time.Second) {
					t.Fatal("the new connection did not receive the reply")
				}
				return
			}
			if err == nil {
				t.Fatal("Process: want an error for the missing connection")
			}
			if receives(t, again, 150*time.Millisecond) {
				t.Fatal("the reply reached a connection that the policy does not allow")
			}
		})
	}
}

// With several candidates the newest connection gets the reply, and a reply
// whose own connection is alive still goes to that connection.
func TestOnMissingConnectionPicksTheNewestAndKeepsExactRouting(t *testing.T) {
	srv, sink := newMissingConnServer(t, OnMissingConnectionSameIP)
	fx := dropAPeer(t, srv, sink)
	older := dialNew(t, srv, 0)
	newer := dialNew(t, srv, 1)

	if _, err := srv.Process(context.Background(), replyFor(fx.lostID, "127.0.0.1")); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !receives(t, newer, 2*time.Second) {
		t.Fatal("the newest connection did not receive the reply")
	}
	if receives(t, older, 150*time.Millisecond) {
		t.Fatal("the older connection received the reply too")
	}

	// A reply for a live connection is not redirected.
	var liveID string
	srv.conns.Range(func(k, v any) bool {
		if v.(*Connection).conn.RemoteAddr().String() == older.LocalAddr().String() {
			liveID = k.(string)
		}
		return true
	})
	if liveID == "" {
		t.Fatal("could not find the live connection")
	}
	if _, err := srv.Process(context.Background(), replyFor(liveID, "127.0.0.1")); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !receives(t, older, 2*time.Second) {
		t.Fatal("the live connection did not receive its own reply")
	}
	if receives(t, newer, 150*time.Millisecond) {
		t.Fatal("a reply for a live connection was redirected")
	}
}

func TestOnMissingConnectionValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeServer
	cfg.Bind = ":0"
	cfg.OnMissingConnection = "anywhere"
	if err := cfg.Validate(); err == nil {
		t.Error("an unknown policy must be refused")
	}
	cfg.Mode = ModeClient
	cfg.Connect = "127.0.0.1:1"
	cfg.OnMissingConnection = OnMissingConnectionSameIP
	if err := cfg.Validate(); err == nil {
		t.Error("a policy other than drop must be refused in client mode")
	}
	cfg.OnMissingConnection = OnMissingConnectionDrop
	if err := cfg.Validate(); err != nil {
		t.Errorf("drop in client mode: %v", err)
	}
	var zero Config
	zero.Mode = ModeServer
	zero.ApplyDefaults()
	if zero.OnMissingConnection != OnMissingConnectionDrop {
		t.Errorf("default = %q, want drop", zero.OnMissingConnection)
	}
}
