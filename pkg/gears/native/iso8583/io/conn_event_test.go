// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// recordingEmitter captures what a gear emits per port.
type recordingEmitter struct {
	mu   sync.Mutex
	msgs []*fluxmsg.FluxMsg
	port []string
}

func (r *recordingEmitter) Emit(port string, msg *fluxmsg.FluxMsg) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.port = append(r.port, port)
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *recordingEmitter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *recordingEmitter) at(i int) (string, *fluxmsg.FluxMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port[i], r.msgs[i]
}

type eventGearContext struct {
	MockGearContext
	em *recordingEmitter
}

func (e *eventGearContext) Emitter() sdk.PortEmitter { return e.em }

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
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

func connCount(g *Gear) int {
	n := 0
	g.server.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

func startEventGear(t *testing.T, enabled bool) (*Gear, *recordingEmitter) {
	t.Helper()
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	em := &recordingEmitter{}
	ctx := &eventGearContext{
		MockGearContext: MockGearContext{
			config: map[string]any{
				"mode":             "server",
				"bind":             "127.0.0.1:0",
				"emit_conn_events": enabled,
			},
			logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			idGen:  gen,
		},
		em: em,
	}
	g := &Gear{}
	if err := g.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background(), func(*fluxmsg.FluxMsg) {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	return g, em
}

func TestConnEvents_AcceptAndClose(t *testing.T) {
	g, em := startEventGear(t, true)

	c, err := net.Dial("tcp", g.server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "up event", func() bool { return em.count() >= 1 })

	port, up := em.at(0)
	if port != PortConn {
		t.Errorf("port = %q, want %q", port, PortConn)
	}
	if up.Metadata["conn.event"] != "up" {
		t.Errorf("conn.event = %q, want up", up.Metadata["conn.event"])
	}
	connID := up.Metadata["conn.id"]
	if connID == "" {
		t.Fatal("up event carries no conn.id")
	}
	if up.Metadata["peer.ip"] != "127.0.0.1" || up.Metadata["peer.port"] == "" {
		t.Errorf("peer metadata = %v", up.Metadata)
	}
	if len(up.RawPayload) != 0 {
		t.Errorf("event must carry no payload, got %d bytes", len(up.RawPayload))
	}
	// The conn.id in the event must be usable for egress right away.
	if _, ok := g.server.conns.Load(connID); !ok {
		t.Errorf("conn %s not registered when the up event was emitted", connID)
	}

	_ = c.Close()
	waitFor(t, "down event", func() bool { return em.count() >= 2 })
	port, down := em.at(1)
	if port != PortConn || down.Metadata["conn.event"] != "down" || down.Metadata["conn.id"] != connID {
		t.Errorf("down event = port %q meta %v, want conn %s down", port, down.Metadata, connID)
	}
	if _, ok := g.server.conns.Load(connID); ok {
		t.Errorf("conn %s still registered at the down event", connID)
	}
}

func TestConnEvents_OffByDefault(t *testing.T) {
	if DefaultConfig().EmitConnEvents {
		t.Fatal("emit_conn_events must default to false")
	}
	g, em := startEventGear(t, false)

	c, err := net.Dial("tcp", g.server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connection registered", func() bool { return connCount(g) == 1 })
	_ = c.Close()
	waitFor(t, "connection removed", func() bool { return connCount(g) == 0 })
	if n := em.count(); n != 0 {
		t.Errorf("emitted %d events with emit_conn_events off", n)
	}
}
