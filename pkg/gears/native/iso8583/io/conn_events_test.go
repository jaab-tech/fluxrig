// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/events"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// connRecord is one connection event, with what a server's table held at the
// moment of the event.
type connRecord struct {
	event, connID, reason string
	inTable               bool
}

type connRecorder struct {
	mu   sync.Mutex
	recs []connRecord
}

func (r *connRecorder) add(rec connRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func (r *connRecorder) all() []connRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]connRecord(nil), r.recs...)
}

func (r *connRecorder) of(event string) []connRecord {
	var out []connRecord
	for _, rec := range r.all() {
		if rec.event == event {
			out = append(out, rec)
		}
	}
	return out
}

// waitClosed polls until a closed event arrives and returns the record. A short
// settle would hide a second closed event, so the callers read of(Closed) again
// after the handler has finished.
func (r *connRecorder) waitClosed(t *testing.T) connRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c := r.of(events.Closed); len(c) > 0 {
			return c[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no closed event: %+v", r.all())
	return connRecord{}
}

func eventServer(t *testing.T, tune func(*Config)) (*Server, *connRecorder) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode = ModeServer
	cfg.Bind = "127.0.0.1:0"
	if tune != nil {
		tune(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	sink := &tpduSink{}
	srv := NewServer(&cfg, slog.New(slog.DiscardHandler), sink.emit, gen)
	rec := &connRecorder{}
	srv.OnConnEvent(func(event, connID string, _ net.Addr, reason string) {
		_, inTable := srv.conns.Load(connID)
		rec.add(connRecord{event: event, connID: connID, reason: reason, inTable: inTable})
	})
	if err = srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, rec
}

func TestConnEvents_OpenedComesAfterTheConnectionIsInTheTable(t *testing.T) {
	srv, rec := eventServer(t, nil)
	dialNew(t, srv, 0)

	waitOpened(t, rec)
	opened := rec.of(events.Opened)[0]
	if !opened.inTable {
		t.Fatal("opened came before the connection was in the table: a reply to it would fail")
	}
}

func waitOpened(t *testing.T, rec *connRecorder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.of(events.Opened)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no opened event")
}

func TestConnEvents_ServerReasons(t *testing.T) {
	tests := []struct {
		name  string
		tune  func(*Config)
		cause func(t *testing.T, srv *Server, c net.Conn, connID string)
		want  string
	}{
		{
			name: "peer closes",
			cause: func(_ *testing.T, _ *Server, c net.Conn, _ string) {
				_ = c.Close()
			},
			want: events.ReasonPeerClosed,
		},
		{
			name: "idle",
			tune: func(cfg *Config) { cfg.IdleTimeout = Duration(100 * time.Millisecond) },
			cause: func(*testing.T, *Server, net.Conn, string) {
			},
			want: events.ReasonIdleTimeout,
		},
		{
			name: "read error",
			cause: func(t *testing.T, _ *Server, c net.Conn, _ string) {
				// A length prefix of zero is no frame.
				if _, err := c.Write([]byte{0, 0}); err != nil {
					t.Fatal(err)
				}
			},
			want: events.ReasonReadError,
		},
		{
			name: "invalid frame",
			cause: func(t *testing.T, _ *Server, c net.Conn, _ string) {
				// Three bytes are too short for the heuristic validation.
				if _, err := c.Write([]byte{0, 3, 1, 2, 3}); err != nil {
					t.Fatal(err)
				}
			},
			want: events.ReasonInvalidFrame,
		},
		{
			name: "command",
			cause: func(t *testing.T, srv *Server, _ net.Conn, connID string) {
				if err := srv.Disconnect(connID); err != nil {
					t.Fatal(err)
				}
			},
			want: events.ReasonClosedByCommand,
		},
		{
			name: "stop",
			cause: func(t *testing.T, srv *Server, _ net.Conn, _ string) {
				if err := srv.Stop(); err != nil {
					t.Fatal(err)
				}
			},
			want: events.ReasonStopped,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := eventServer(t, tt.tune)
			c := dialNew(t, srv, 0)
			waitOpened(t, rec)

			tt.cause(t, srv, c, rec.of(events.Opened)[0].connID)

			closed := rec.waitClosed(t)
			if closed.reason != tt.want {
				t.Fatalf("reason = %q, want %q", closed.reason, tt.want)
			}
			if closed.inTable {
				t.Fatal("closed came while the connection was still in the table")
			}
			waitConns(t, srv, 0)
			if err := srv.waitConnections(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			if n := len(rec.of(events.Closed)); n != 1 {
				t.Fatalf("closed events = %d, want 1", n)
			}
		})
	}
}

// A drained connection stays open for replies, and closes at Stop.
func TestConnEvents_DrainThenStopReportsStopped(t *testing.T) {
	srv, rec := eventServer(t, nil)
	dialNew(t, srv, 0)
	waitOpened(t, rec)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.of(events.Closed)); n != 0 {
		t.Fatalf("a drain closed a connection: %+v", rec.all())
	}
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := rec.waitClosed(t).reason; got != events.ReasonStopped {
		t.Fatalf("reason = %q, want %q", got, events.ReasonStopped)
	}
}

func eventClient(t *testing.T, tune func(*Config)) (*Client, *connRecorder, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cfg := DefaultConfig()
	cfg.Mode = ModeClient
	cfg.Connect = l.Addr().String()
	cfg.ReconnectWait = Duration(time.Hour)
	if tune != nil {
		tune(&cfg)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	sink := &tpduSink{}
	client := NewClient(&cfg, slog.New(slog.DiscardHandler), sink.emit, gen)
	rec := &connRecorder{}
	client.OnConnEvent(func(event, connID string, _ net.Addr, reason string) {
		rec.add(connRecord{event: event, connID: connID, reason: reason})
	})
	if err = client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	waitOpened(t, rec)
	return client, rec, peer
}

func TestConnEvents_ClientReasons(t *testing.T) {
	t.Run("peer closes", func(t *testing.T) {
		client, rec, peer := eventClient(t, nil)
		t.Cleanup(func() { _ = client.Stop() })
		_ = peer.Close()
		if got := rec.waitClosed(t).reason; got != events.ReasonPeerClosed {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("idle", func(t *testing.T) {
		client, rec, _ := eventClient(t, func(cfg *Config) { cfg.IdleTimeout = Duration(100 * time.Millisecond) })
		t.Cleanup(func() { _ = client.Stop() })
		if got := rec.waitClosed(t).reason; got != events.ReasonIdleTimeout {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("command", func(t *testing.T) {
		client, rec, _ := eventClient(t, nil)
		t.Cleanup(func() { _ = client.Stop() })
		if err := client.Disconnect(rec.of(events.Opened)[0].connID); err != nil {
			t.Fatal(err)
		}
		if got := rec.waitClosed(t).reason; got != events.ReasonClosedByCommand {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("stop waits for the closed event", func(t *testing.T) {
		client, rec, _ := eventClient(t, nil)
		if err := client.Stop(); err != nil {
			t.Fatal(err)
		}
		closed := rec.of(events.Closed)
		if len(closed) != 1 {
			t.Fatalf("Stop returned before the closed event: %+v", rec.all())
		}
		if closed[0].reason != events.ReasonStopped {
			t.Fatalf("reason = %q", closed[0].reason)
		}
	})
}

// eventContext is a gear context on a Rack: it can publish events.
type eventContext struct {
	sdk.GearContext
	mu  sync.Mutex
	got []events.ConnEvent
}

func (c *eventContext) RackName() string         { return "rack1" }
func (c *eventContext) GearRack(_ string) string { return "rack1" }
func (c *eventContext) PublishEvent(_ context.Context, _ string, data []byte, _ uuid.UUID) error {
	var ev events.ConnEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ev)
	return nil
}

func (c *eventContext) events() []events.ConnEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.ConnEvent(nil), c.got...)
}

func startEventGear(t *testing.T, ctx sdk.GearContext) *Gear {
	t.Helper()
	g := &Gear{}
	if err := g.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background(), func(*fluxmsg.FluxMsg) {}); err != nil {
		t.Fatal(err)
	}
	return g
}

// Stop must leave the closed events published: nothing else sends them.
func TestGear_PublishesEventsAndFlushesThemAtStop(t *testing.T) {
	ctx := &eventContext{GearContext: sdk.NewMockGearContext(map[string]any{"mode": "server", "bind": "127.0.0.1:0"})}
	g := startEventGear(t, ctx)

	c, err := net.Dial("tcp", g.server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	waitConns(t, g.server, 1)

	if err = g.Stop(); err != nil {
		t.Fatal(err)
	}
	got := ctx.events()
	if len(got) != 2 {
		t.Fatalf("events = %+v, want opened and closed", got)
	}
	if got[0].Event != events.Opened || got[1].Event != events.Closed || got[1].Reason != events.ReasonStopped {
		t.Fatalf("events = %+v", got)
	}
	if got[0].PeerIP != "127.0.0.1" || got[0].PeerPort == "" || got[0].Gear != "mock_gear" || got[0].Rack != "rack1" {
		t.Fatalf("event identity = %+v", got[0])
	}
}

func TestGear_PublishEventsOff(t *testing.T) {
	ctx := &eventContext{GearContext: sdk.NewMockGearContext(map[string]any{"mode": "server", "bind": "127.0.0.1:0", "publish_events": false})}
	g := startEventGear(t, ctx)
	defer func() { _ = g.Stop() }()
	if g.events != nil {
		t.Fatal("publish_events false started a publisher")
	}
}

// A context that is not on a Rack has nowhere to publish, and that is no error.
func TestGear_WithoutAnEventProviderPublishesNothing(t *testing.T) {
	g := startEventGear(t, sdk.NewMockGearContext(map[string]any{"mode": "server", "bind": "127.0.0.1:0"}))
	defer func() { _ = g.Stop() }()
	if g.events != nil {
		t.Fatal("a context with no event provider started a publisher")
	}
}
