// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package conductor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/valet"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

type emitRec struct {
	port string
	msg  *fluxmsg.FluxMsg
}

type capEmitter struct {
	mu       sync.Mutex
	emits    []emitRec
	failNext int // Emit fails this many more times before it starts succeeding.
}

func (c *capEmitter) Emit(port string, m *fluxmsg.FluxMsg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext > 0 {
		c.failNext--
		return errors.New("synthetic emit failure")
	}
	c.emits = append(c.emits, emitRec{port: port, msg: m})
	return nil
}

func (c *capEmitter) take() []emitRec {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.emits
	c.emits = nil
	return out
}

type mockCtx struct {
	cfg      map[string]any
	emitter  sdk.PortEmitter
	bindings map[string]sdk.PortBinding
	cp       any
}

func (m *mockCtx) Bindings() map[string]sdk.PortBinding { return m.bindings }

func (m *mockCtx) Context() context.Context { return context.Background() }
func (m *mockCtx) Config() map[string]any   { return m.cfg }
func (m *mockCtx) GearName() string         { return "conductor-test" }
func (m *mockCtx) MachineID() uuid.UUID     { return uuid.Nil }
func (m *mockCtx) Logger() *slog.Logger     { return slog.Default() }
func (m *mockCtx) IDGen() sdk.IDGenerator   { return nil }
func (m *mockCtx) Bus() bus.Bus             { return nil }
func (m *mockCtx) Manager() manager.Manager { return nil }
func (m *mockCtx) ControlPlane() any        { return m.cp }
func (m *mockCtx) ClusterPublicKey() []byte { return nil }
func (m *mockCtx) Emitter() sdk.PortEmitter { return m.emitter }

// fakeCP is an in-memory control plane for link-state tests.
type fakeCP struct {
	mu    sync.Mutex
	chans map[string]chan ctrl.Command
}

func newFakeCP() *fakeCP { return &fakeCP{chans: make(map[string]chan ctrl.Command)} }

func (f *fakeCP) Publish(target string, cmd ctrl.Command) error {
	f.mu.Lock()
	ch := f.chans[target]
	f.mu.Unlock()
	if ch != nil {
		ch <- cmd
	}
	return nil
}

func (f *fakeCP) Subscribe(id string) (<-chan ctrl.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan ctrl.Command, 16)
	f.chans[id] = ch
	return ch, nil
}

// Minimal deterministic clock (mirrors the valet test clock).
type fakeTimer struct {
	c       *fakeClock
	when    time.Time
	fn      func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) valet.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, when: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		var next *fakeTimer
		for _, t := range c.timers {
			if t.fired || t.stopped || t.when.After(target) {
				continue
			}
			if next == nil || t.when.Before(next.when) {
				next = t
			}
		}
		if next == nil {
			break
		}
		c.now = next.when
		next.fired = true
		fn := next.fn
		c.mu.Unlock()
		fn()
		c.mu.Lock()
	}
	c.now = target
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func switchConfig() map[string]any {
	return map[string]any{
		"correlation_key": []any{"11", "41"},
		"park_fields":     []any{"62"},
		"valet": map[string]any{
			"store":              "memory",
			"default_ttl":        "30s",
			"retain_after_close": "5s",
		},
		"routes": []any{
			map[string]any{
				"name":  "scheme-a",
				"match": map[string]any{"field": "2", "prefix": "4"},
				"destination": map[string]any{
					"failover": []any{
						map[string]any{"least_loaded": []any{"out_scheme_a", "out_scheme_a2"}},
						"out_west",
					},
				},
			},
			map[string]any{
				"name":        "scheme-b",
				"match":       map[string]any{"field": "2", "prefix": "5"},
				"destination": map[string]any{"failover": []any{"out_scheme_b", "out_west"}},
			},
		},
	}
}

func newTestGear(t *testing.T, clk valet.Clock, avail func(string) bool) (*Gear, *capEmitter) {
	t.Helper()
	em := &capEmitter{}
	g := New()
	g.clock = clk
	g.availability = avail
	if err := g.Init(&mockCtx{cfg: switchConfig(), emitter: em}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	return g, em
}

func request(stan string) *fluxmsg.FluxMsg {
	return &fluxmsg.FluxMsg{
		Data: map[string]any{
			"2":  "4111111111111111",
			"11": stan,
			"41": "TERM0001",
			"62": "routing-hint",
		},
		Metadata: map[string]string{"conn.id": "terminal-7"},
	}
}

func reply(stan string) *fluxmsg.FluxMsg {
	return &fluxmsg.FluxMsg{
		Data: map[string]any{
			"11": stan,
			"41": "TERM0001",
			"39": "00",
		},
		Metadata: map[string]string{"conn.id": "scheme-socket-3"},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRequestRoutedAndParked(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	if err := g.ProcessPort(ctx, "in", request("000001")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || !strings.HasPrefix(emits[0].port, "out_scheme_a") {
		t.Fatalf("emits = %+v, want one on the local scheme-a pool", emits)
	}
	if _, parked := emits[0].msg.Data["62"]; parked {
		t.Fatalf("park field 62 leaked onto the outbound leg")
	}
	if g.engine.InFlight() != 1 {
		t.Fatalf("InFlight = %d, want 1", g.engine.InFlight())
	}
}

func TestReplyRestoredAndRoutedHome(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	if err := g.ProcessPort(ctx, "in", request("000002")); err != nil {
		t.Fatalf("in: %v", err)
	}
	em.take()

	if err := g.ProcessPort(ctx, "in_reply", reply("000002")); err != nil {
		t.Fatalf("in_reply: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_response" {
		t.Fatalf("emits = %+v, want one on out_response", emits)
	}
	resp := emits[0].msg
	if resp.Data["62"] != "routing-hint" {
		t.Fatalf("parked field not restored: %v", resp.Data)
	}
	if resp.Metadata["conn.id"] != "terminal-7" {
		t.Fatalf("return context must win: conn.id = %q", resp.Metadata["conn.id"])
	}
	if g.engine.InFlight() != 0 {
		t.Fatalf("InFlight = %d after reply, want 0", g.engine.InFlight())
	}
}

func TestRetransmissionAbsorbed(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	if err := g.ProcessPort(ctx, "in", request("000003")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := g.ProcessPort(ctx, "in", request("000003")); err != nil {
		t.Fatalf("retransmission: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 {
		t.Fatalf("emits = %d, want exactly 1 (no double routing)", len(emits))
	}
}

// TestFailedEmitReleasesTicketForRetry is a regression test for the finding
// that a failed emit on a freshly parked ticket left it stuck open: a retry
// of the same request (e.g. a bus redelivery after the failed emit made
// ProcessPort return an error) hit AttachedOpen and was absorbed as a
// harmless duplicate, even though nothing was ever actually sent. The
// request then silently waited out its full TTL instead of being retried.
func TestFailedEmitReleasesTicketForRetry(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()
	em.failNext = 1

	if err := g.ProcessPort(ctx, "in", request("000099")); err == nil {
		t.Fatal("first attempt: want the emit failure to surface as an error")
	}
	if emits := em.take(); len(emits) != 0 {
		t.Fatalf("emits after failed send = %+v, want none", emits)
	}
	if g.engine.InFlight() != 0 {
		t.Fatalf("InFlight = %d after a failed emit, want 0 (ticket released)", g.engine.InFlight())
	}

	// Simulate the bus redelivering the same original request.
	if err := g.ProcessPort(ctx, "in", request("000099")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 {
		t.Fatalf("emits after retry = %d, want exactly 1 (the retry actually sent, not absorbed)", len(emits))
	}
	if g.engine.InFlight() != 1 {
		t.Fatalf("InFlight = %d after retry, want 1", g.engine.InFlight())
	}
}

func TestReplayAfterRedemption(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	if err := g.ProcessPort(ctx, "in", request("000004")); err != nil {
		t.Fatalf("in: %v", err)
	}
	if err := g.ProcessPort(ctx, "in_reply", reply("000004")); err != nil {
		t.Fatalf("reply: %v", err)
	}
	first := em.take()

	// The terminal missed the response and retransmits: replay, do not re-route.
	if err := g.ProcessPort(ctx, "in", request("000004")); err != nil {
		t.Fatalf("replayed request: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_response" {
		t.Fatalf("emits = %+v, want a single out_response replay", emits)
	}
	orig := first[len(first)-1].msg
	// The replay reproduces the response content on the same return path...
	if got, want := emits[0].msg.Data["39"], orig.Data["39"]; got != want {
		t.Fatalf("replay DE39 = %v, want %v (retained outcome)", got, want)
	}
	if got, want := emits[0].msg.Data["11"], orig.Data["11"]; got != want {
		t.Fatalf("replay STAN = %v, want %v", got, want)
	}
	// ...but as an independent clone: sharing the retained pointer would race
	// concurrent replays and the live reply path (finding C5).
	if emits[0].msg == orig {
		t.Fatalf("replay must emit an independent clone, not the retained pointer")
	}
}

func TestNoRoute(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)

	m := request("000005")
	m.Data["2"] = "9999000011112222" // no route matches this range
	if err := g.ProcessPort(context.Background(), "in", m); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "error" || emits[0].msg.Metadata["error.reason"] != "no_route" {
		t.Fatalf("emits = %+v, want error/no_route", emits)
	}
}

func TestNoDestinationParksNothing(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), func(string) bool { return false })

	if err := g.ProcessPort(context.Background(), "in", request("000006")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "error" || emits[0].msg.Metadata["error.reason"] != "no_destination" {
		t.Fatalf("emits = %+v, want error/no_destination", emits)
	}
	if g.engine.InFlight() != 0 {
		t.Fatalf("no ticket may be parked when nothing is routable")
	}
}

func TestFailoverToPeerWhenLocalPoolDown(t *testing.T) {
	localDown := func(p string) bool { return p == "out_west" }
	g, em := newTestGear(t, newFakeClock(), localDown)

	if err := g.ProcessPort(context.Background(), "in", request("000007")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_west" {
		t.Fatalf("emits = %+v, want out_west (cross-region failover)", emits)
	}
	if g.engine.InFlightTo("out_west") != 1 {
		t.Fatalf("in-flight accounting must follow the chosen destination")
	}
}

func TestTimeoutSurfacesOriginalOnErrorPort(t *testing.T) {
	clk := newFakeClock()
	g, em := newTestGear(t, clk, nil)

	if err := g.ProcessPort(context.Background(), "in", request("000008")); err != nil {
		t.Fatalf("in: %v", err)
	}
	em.take()

	clk.Advance(31 * time.Second) // past the 30s ticket TTL

	emits := em.take()
	if len(emits) != 1 || emits[0].port != "error" {
		t.Fatalf("emits = %+v, want one on error", emits)
	}
	msg := emits[0].msg
	if msg.Metadata["error.reason"] != "timeout" {
		t.Fatalf("reason = %q, want timeout", msg.Metadata["error.reason"])
	}
	if msg.Data["62"] != "routing-hint" {
		t.Fatalf("timeout must carry the FULL original request (parked copy)")
	}
	if msg.Metadata["conn.id"] != "terminal-7" {
		t.Fatalf("timeout must carry the return context")
	}
}

func TestUnmatchedReply(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)

	if err := g.ProcessPort(context.Background(), "in_reply", reply("999999")); err != nil {
		t.Fatalf("in_reply: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "error" || emits[0].msg.Metadata["error.reason"] != "unmatched_reply" {
		t.Fatalf("emits = %+v, want error/unmatched_reply", emits)
	}
}

func TestMissingCorrelationField(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)

	m := request("000009")
	delete(m.Data, "11")
	if err := g.ProcessPort(context.Background(), "in", m); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "error" || emits[0].msg.Metadata["error.reason"] != "no_key" {
		t.Fatalf("emits = %+v, want error/no_key", emits)
	}
}

// A conductor with a mesh identity stamps every routed request in-band.
func TestOriginStampedOnOutbound(t *testing.T) {
	em := &capEmitter{}
	g := New()
	g.clock = newFakeClock()
	cfg := switchConfig()
	cfg["origin"] = "east"
	if err := g.Init(&mockCtx{cfg: cfg, emitter: em}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })

	if err := g.ProcessPort(context.Background(), "in", request("000020")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].msg.Metadata["conductor.origin"] != "east" {
		t.Fatalf("emits = %+v, want outbound stamped conductor.origin=east", emits)
	}
}

// A request handed off by a peer returns on out_response_<origin>, and a
// replayed retransmission takes the same return path.
func TestPeerResponseRouting(t *testing.T) {
	em := &capEmitter{}
	g := New()
	g.clock = newFakeClock()
	cfg := switchConfig()
	cfg["origin"] = "west"
	if err := g.Init(&mockCtx{cfg: cfg, emitter: em}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	ctx := context.Background()

	m := request("000021")
	m.Metadata["conductor.origin"] = "east" // stamped by the forwarding peer
	if err := g.ProcessPort(ctx, "in", m); err != nil {
		t.Fatalf("in: %v", err)
	}
	em.take()

	if err := g.ProcessPort(ctx, "in_reply", reply("000021")); err != nil {
		t.Fatalf("reply: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_response_east" {
		t.Fatalf("emits = %+v, want out_response_east", emits)
	}

	// Replay of the same handed-off request must reuse the peer return path.
	m2 := request("000021")
	m2.Metadata["conductor.origin"] = "east"
	if err := g.ProcessPort(ctx, "in", m2); err != nil {
		t.Fatalf("replayed request: %v", err)
	}
	emits = em.take()
	if len(emits) != 1 || emits[0].port != "out_response_east" {
		t.Fatalf("replay emits = %+v, want out_response_east", emits)
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// Ports bound to a local I/O gear follow that gear's link-state: down until
// the socket reports up, and traffic fails over while a leg is down.
func TestLinkStateDrivesAvailability(t *testing.T) {
	em := &capEmitter{}
	cp := newFakeCP()
	g := New()
	g.clock = newFakeClock()
	mc := &mockCtx{
		cfg:     switchConfig(),
		emitter: em,
		cp:      cp,
		bindings: map[string]sdk.PortBinding{
			"out_scheme_a":  {Kind: sdk.BindingIO, Gear: "uplink-a"},
			"out_scheme_a2": {Kind: sdk.BindingIO, Gear: "uplink-a2"},
			"out_west":      {Kind: sdk.BindingRemote, Gear: "conductor-west"},
		},
	}
	if err := g.Init(mc); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := g.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	ctx := context.Background()

	// Both local sockets start down: traffic fails over to the peer.
	if err := g.ProcessPort(ctx, "in", request("000030")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_west" {
		t.Fatalf("emits = %+v, want out_west while local pool is down", emits)
	}

	// The first socket comes up: local traffic resumes on that leg.
	_ = cp.Publish("link.uplink-a", ctrl.Command{Cmd: "conn.up", Args: map[string]string{"conn_id": "c1"}})
	waitUntil(t, func() bool { return g.availability("out_scheme_a") }, "out_scheme_a up")

	if err := g.ProcessPort(ctx, "in", request("000031")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits = em.take()
	if len(emits) != 1 || emits[0].port != "out_scheme_a" {
		t.Fatalf("emits = %+v, want out_scheme_a after conn.up", emits)
	}

	// The socket drops again: back to the failover child.
	_ = cp.Publish("link.uplink-a", ctrl.Command{Cmd: "conn.down", Args: map[string]string{"conn_id": "c1"}})
	waitUntil(t, func() bool { return !g.availability("out_scheme_a") }, "out_scheme_a down")

	if err := g.ProcessPort(ctx, "in", request("000032")); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits = em.take()
	if len(emits) != 1 || emits[0].port != "out_west" {
		t.Fatalf("emits = %+v, want out_west after conn.down", emits)
	}
}

// The conductor must resolve correlation keys, match fields and park fields
// as dotted paths, so it works against real codec output (Data["iso8583"]
// ["field"]["11"], not a flat "11").
func TestNestedCodecFieldPaths(t *testing.T) {
	em := &capEmitter{}
	g := New()
	g.clock = newFakeClock()
	cfg := map[string]any{
		"correlation_key": []any{"iso8583.field.11", "iso8583.field.41"},
		"park_fields":     []any{"iso8583.field.62"},
		"routes": []any{
			map[string]any{
				"name":        "scheme-a",
				"match":       map[string]any{"field": "iso8583.field.2", "prefix": "4"},
				"destination": map[string]any{"failover": []any{"out_scheme_a"}},
			},
		},
	}
	if err := g.Init(&mockCtx{cfg: cfg, emitter: em}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	ctx := context.Background()

	// Build the request as the codec would: nested field tree.
	req := fluxmsg.New()
	_ = req.Set("iso8583.field.2", "4111111111111111")
	_ = req.Set("iso8583.field.11", "000100")
	_ = req.Set("iso8583.field.41", "TERM0001")
	_ = req.Set("iso8583.field.62", "routing-hint")
	req.Metadata["conn.id"] = "term-1"

	if err := g.ProcessPort(ctx, "in", req); err != nil {
		t.Fatalf("in: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_scheme_a" {
		t.Fatalf("emits = %+v, want out_scheme_a (nested match)", emits)
	}
	// Park field stripped from the outbound copy; empty parent pruned.
	if _, ok := emits[0].msg.Get("iso8583.field.62"); ok {
		t.Fatalf("park field 62 leaked onto the outbound leg")
	}

	// Reply with matching nested key fields.
	rep := fluxmsg.New()
	_ = rep.Set("iso8583.field.11", "000100")
	_ = rep.Set("iso8583.field.41", "TERM0001")
	_ = rep.Set("iso8583.field.39", "00")
	if err := g.ProcessPort(ctx, "in_reply", rep); err != nil {
		t.Fatalf("reply: %v", err)
	}
	emits = em.take()
	if len(emits) != 1 || emits[0].port != "out_response" {
		t.Fatalf("emits = %+v, want out_response", emits)
	}
	if v, ok := emits[0].msg.Get("iso8583.field.62"); !ok || v != "routing-hint" {
		t.Fatalf("parked field not restored on reply: %v", emits[0].msg.Data)
	}
}

func TestInitValidation(t *testing.T) {
	em := &capEmitter{}

	cfg := switchConfig()
	delete(cfg, "correlation_key")
	if err := New().Init(&mockCtx{cfg: cfg, emitter: em}); err == nil {
		t.Fatalf("missing correlation_key accepted")
	}

	cfg = switchConfig()
	cfg["valet"].(map[string]any)["store"] = "shared"
	if err := New().Init(&mockCtx{cfg: cfg, emitter: em}); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unimplemented store must fail loudly, got: %v", err)
	}

	cfg = switchConfig()
	delete(cfg, "routes")
	if err := New().Init(&mockCtx{cfg: cfg, emitter: em}); err == nil {
		t.Fatalf("missing routes accepted")
	}
}

func TestDrainKeepsMatchingReplies(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	if err := g.ProcessPort(ctx, "in", request("000010")); err != nil {
		t.Fatalf("in: %v", err)
	}
	em.take()

	done := make(chan error, 1)
	go func() { done <- g.Drain(context.Background()) }()

	// The open ticket still matches during the drain.
	if err := g.ProcessPort(ctx, "in_reply", reply("000010")); err != nil {
		t.Fatalf("reply during drain: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("drain did not complete after tickets cleared")
	}
	emits := em.take()
	if len(emits) != 1 || emits[0].port != "out_response" {
		t.Fatalf("emits = %+v, want the matched response", emits)
	}
}

func TestStripParkedMapAnyAny(t *testing.T) {
	// A parked field nested under a map[any]any parent (as a CBOR round trip
	// can yield) must be removed from the outbound copy, not silently shared.
	root := map[string]any{
		"iso8583": map[any]any{
			"field": map[any]any{"62": "SECRET", "11": "000009"},
		},
	}
	deletePath(root, "iso8583.field.62")
	inner := root["iso8583"].(map[any]any)["field"].(map[any]any)
	if _, ok := inner["62"]; ok {
		t.Fatalf("park field under map[any]any not stripped: %+v", inner)
	}
	if _, ok := inner["11"]; !ok {
		t.Fatalf("sibling field wrongly removed: %+v", inner)
	}
}

// TestReplyMTINotClobberedByReturnContext locks in the fix for finding B4: the
// return context carries connection routing (conn.id), not the request's
// protocol fields, so a reply's own MTI/response code survive on the way home.
func TestReplyMTINotClobberedByReturnContext(t *testing.T) {
	g, em := newTestGear(t, newFakeClock(), nil)
	ctx := context.Background()

	req := request("000010")
	req.Metadata["iso8583.mti"] = "0200"
	if err := g.ProcessPort(ctx, "in", req); err != nil {
		t.Fatalf("in: %v", err)
	}
	em.take()

	rep := reply("000010")
	rep.Metadata["iso8583.mti"] = "0210" // the scheme's response MTI
	if err := g.ProcessPort(ctx, "in_reply", rep); err != nil {
		t.Fatalf("in_reply: %v", err)
	}
	emits := em.take()
	if len(emits) != 1 {
		t.Fatalf("emits = %+v, want one response", emits)
	}
	if got := emits[0].msg.Metadata["iso8583.mti"]; got != "0210" {
		t.Fatalf("response MTI = %q, want 0210 (reply MTI must survive the return context)", got)
	}
	// The return context still routes home: the origin conn.id wins.
	if got := emits[0].msg.Metadata["conn.id"]; got != "terminal-7" {
		t.Fatalf("conn.id = %q, want the origin terminal-7", got)
	}
}

// TestResponsePortFor is a regression test for a real bug: the origin stamp a
// reply's return context carries arrives in the request's own metadata, set
// by whoever sent it, never authenticated, and responsePortFor used to build
// an output port name from it unfiltered. A malicious or misconfigured peer
// could name any string as the port to route its reply to, and since ports
// are lazily created for any name seen, an unbounded source of new ones too.
func TestResponsePortFor(t *testing.T) {
	g := &Gear{origin: "self"}

	cases := []struct {
		name      string
		returnCtx map[string]string
		known     []string
		wantPort  string
		wantErr   bool
	}{
		{"no origin stamp: the plain response port", map[string]string{}, nil, portResponse, false},
		{"this gear's own origin: the plain response port", map[string]string{metaOrigin: "self"}, nil, portResponse, false},
		{"valid peer, no allowlist configured", map[string]string{metaOrigin: "peer-A"}, nil, portResponse + "_peer-A", false},
		{"valid peer, allowlisted", map[string]string{metaOrigin: "peer-A"}, []string{"peer-A", "peer-B"}, portResponse + "_peer-A", false},
		{"valid format but not an allowlisted peer", map[string]string{metaOrigin: "peer-Z"}, []string{"peer-A"}, "", true},
		{"a dot would be read as a rack/gear/port separator", map[string]string{metaOrigin: "evil.rack.gear"}, nil, "", true},
		{"the old (false) 'cannot occur' separator byte", map[string]string{metaOrigin: "evil\x1fbyte"}, nil, "", true},
		{"absurdly long is refused, not silently truncated or accepted", map[string]string{metaOrigin: strings.Repeat("a", 200)}, nil, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g.knownOrigins = nil
			if tc.known != nil {
				g.knownOrigins = make(map[string]bool, len(tc.known))
				for _, k := range tc.known {
					g.knownOrigins[k] = true
				}
			}
			port, err := g.responsePortFor(&valet.Ticket{ReturnCtx: tc.returnCtx})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got port %q", port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if port != tc.wantPort {
				t.Fatalf("port = %q, want %q", port, tc.wantPort)
			}
		})
	}
}

// TestBuildKeyNoSeparatorCollisionForBinaryFields is a regression test for a
// real bug: buildKey joined correlation-key parts with "\x1f" on the claim
// that byte "cannot occur in structured field values of the supported
// dialects" -- false for a binary field (a PIN block, a MAC, an EMV tag),
// which can contain any byte at all. Two different correlation tuples must
// never join to the same key.
func TestBuildKeyNoSeparatorCollisionForBinaryFields(t *testing.T) {
	g := &Gear{corrKey: []string{"a", "b"}}

	msg1 := fluxmsg.New()
	_ = msg1.Set("a", []byte("A\x1fB"))
	_ = msg1.Set("b", []byte("C"))

	msg2 := fluxmsg.New()
	_ = msg2.Set("a", []byte("A"))
	_ = msg2.Set("b", []byte("B\x1fC"))

	key1, err := g.buildKey(msg1)
	if err != nil {
		t.Fatalf("buildKey(msg1): %v", err)
	}
	key2, err := g.buildKey(msg2)
	if err != nil {
		t.Fatalf("buildKey(msg2): %v", err)
	}
	if key1 == key2 {
		t.Fatalf("buildKey collided: %q == %q for two different correlation tuples", key1, key2)
	}
}

// TestFieldToKeyPartAgreesAcrossGears is a regression test for the other half
// of the same finding: coatcheck's extractKey and the conductor's own key
// building each rendered a []byte field value their own way (fmt.Sprint's
// bracketed decimal list vs a raw byte-to-string cast), so the same field
// value produced two different key parts depending on which gear derived it.
// Both must now agree, since the same field can feed a correlation key on
// either side of a topology that uses both gears.
func TestFieldToKeyPartAgreesAcrossGears(t *testing.T) {
	track2 := []byte{0x82, 0xE7, 0x05, 0x00, 0x1F}
	got := sdk.FieldToKeyPart(track2)
	want := string(track2)
	if got != want {
		t.Fatalf("FieldToKeyPart(%v) = %q, want %q (the raw bytes as a string)", track2, got, want)
	}
}
