// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// ---------------------------------------------------------------------------
// Deterministic clock
// ---------------------------------------------------------------------------

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

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, when: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward, firing due timers in deadline order.
// Callbacks run synchronously on the calling goroutine, outside the clock
// lock, so tests are fully deterministic.
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
// Helpers
// ---------------------------------------------------------------------------

func msg() *fluxmsg.FluxMsg { return &fluxmsg.FluxMsg{} }

func newTestEngine(t *testing.T, clk Clock, mut func(*Config)) *Engine {
	t.Helper()
	cfg := Config{
		DefaultTTL:       30 * time.Second,
		RetainAfterClose: 5 * time.Second,
		Clock:            clk,
	}
	if mut != nil {
		mut(&cfg)
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestParkAndRedeem(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	res, tk, err := e.Park(ctx, ParkRequest{
		Key:         "k1",
		Destination: "out_scheme_a",
		Request:     msg(),
		ReturnCtx:   map[string]string{"conn.id": "c-9"},
	})
	if err != nil || res != Parked {
		t.Fatalf("park = %v, %v; want Parked, nil", res, err)
	}
	if got := e.InFlight(); got != 1 {
		t.Fatalf("InFlight = %d, want 1", got)
	}
	if got := e.InFlightTo("out_scheme_a"); got != 1 {
		t.Fatalf("InFlightTo = %d, want 1", got)
	}
	if tk.Deadline.Sub(tk.ParkedAt) != 30*time.Second {
		t.Fatalf("deadline = %v, want parked+30s", tk.Deadline)
	}

	outcome := msg()
	got, err := e.Redeem(ctx, "k1", outcome)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if got.ReturnCtx["conn.id"] != "c-9" {
		t.Fatalf("return context lost: %v", got.ReturnCtx)
	}
	if got.State() != TicketClosed || got.Outcome != outcome {
		t.Fatalf("state=%v outcome=%p, want closed with retained outcome", got.State(), got.Outcome)
	}
	if e.InFlight() != 0 || e.InFlightTo("out_scheme_a") != 0 {
		t.Fatalf("counts not cleared: %d / %d", e.InFlight(), e.InFlightTo("out_scheme_a"))
	}
}

func TestRetransmissionAttaches(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	_, first, err := e.Park(ctx, ParkRequest{Key: "k1", Destination: "d"})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	res, second, err := e.Park(ctx, ParkRequest{Key: "k1", Destination: "d"})
	if err != nil || res != AttachedOpen {
		t.Fatalf("second park = %v, %v; want AttachedOpen", res, err)
	}
	if second != first {
		t.Fatalf("attach returned a different ticket")
	}
	if e.InFlight() != 1 {
		t.Fatalf("InFlight = %d, want 1 (no duplicate)", e.InFlight())
	}
}

func TestReplayWithinRetentionThenFreshAfterPurge(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	outcome := msg()
	if _, err := e.Redeem(ctx, "k1", outcome); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	res, tk, err := e.Park(ctx, ParkRequest{Key: "k1"})
	if err != nil || res != ReplayedClosed {
		t.Fatalf("park after redeem = %v, %v; want ReplayedClosed", res, err)
	}
	if tk.Outcome != outcome {
		t.Fatalf("replayed outcome mismatch")
	}
	if e.Retained() != 1 {
		t.Fatalf("Retained = %d, want 1", e.Retained())
	}

	clk.Advance(6 * time.Second) // beyond the 5s retention window

	if e.Retained() != 0 {
		t.Fatalf("Retained = %d after purge, want 0", e.Retained())
	}
	res, _, err = e.Park(ctx, ParkRequest{Key: "k1"})
	if err != nil || res != Parked {
		t.Fatalf("park after purge = %v, %v; want Parked (fresh lifecycle)", res, err)
	}
}

func TestRedeemUnmatchedAndDuplicate(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	if _, err := e.Redeem(ctx, "ghost", msg()); err != ErrUnmatched {
		t.Fatalf("redeem unknown = %v, want ErrUnmatched", err)
	}

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != ErrAlreadyRedeemed {
		t.Fatalf("duplicate redeem = %v, want ErrAlreadyRedeemed", err)
	}

	clk.Advance(6 * time.Second) // retention over
	if _, err := e.Redeem(ctx, "k1", msg()); err != ErrUnmatched {
		t.Fatalf("redeem after purge = %v, want ErrUnmatched", err)
	}
}

func TestExpirySurfacesTicketAndLateReplyIsUnmatched(t *testing.T) {
	clk := newFakeClock()
	expired := make(chan *Ticket, 1)
	e := newTestEngine(t, clk, func(c *Config) {
		c.OnExpire = func(tk *Ticket) { expired <- tk }
	})
	ctx := context.Background()

	req := msg()
	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1", Destination: "d", Request: req}); err != nil {
		t.Fatalf("park: %v", err)
	}

	clk.Advance(31 * time.Second) // past the 30s TTL

	select {
	case tk := <-expired:
		if tk.Key != "k1" || tk.Request != req || tk.State() != TicketExpired {
			t.Fatalf("expired ticket wrong: key=%s state=%v", tk.Key, tk.State())
		}
	default:
		t.Fatalf("OnExpire not called")
	}
	if e.InFlight() != 0 || e.InFlightTo("d") != 0 {
		t.Fatalf("counts not cleared on expiry")
	}
	// Expired keys are not retained: a late reply is unmatched.
	if _, err := e.Redeem(ctx, "k1", msg()); err != ErrUnmatched {
		t.Fatalf("late reply = %v, want ErrUnmatched", err)
	}
}

func TestRedeemStopsExpiry(t *testing.T) {
	clk := newFakeClock()
	fired := false
	e := newTestEngine(t, clk, func(c *Config) {
		c.OnExpire = func(*Ticket) { fired = true }
	})
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	clk.Advance(time.Minute)
	if fired {
		t.Fatalf("OnExpire fired for a redeemed ticket")
	}
}

func TestTTLDefaultAndCap(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, func(c *Config) {
		c.DefaultTTL = 30 * time.Second
		c.MaxTTL = time.Minute
	})
	ctx := context.Background()

	_, tk, err := e.Park(ctx, ParkRequest{Key: "a"})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if d := tk.Deadline.Sub(tk.ParkedAt); d != 30*time.Second {
		t.Fatalf("default ttl deadline = %v, want 30s", d)
	}

	_, tk, err = e.Park(ctx, ParkRequest{Key: "b", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if d := tk.Deadline.Sub(tk.ParkedAt); d != time.Minute {
		t.Fatalf("capped ttl deadline = %v, want 1m (MaxTTL)", d)
	}
}

func TestRetentionDisabled(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, func(c *Config) {
		c.RetainAfterClose = -1 // disabled
	})
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// No retention: duplicate reply is unmatched, re-park starts fresh.
	if _, err := e.Redeem(ctx, "k1", msg()); err != ErrUnmatched {
		t.Fatalf("duplicate with retention disabled = %v, want ErrUnmatched", err)
	}
	res, _, err := e.Park(ctx, ParkRequest{Key: "k1"})
	if err != nil || res != Parked {
		t.Fatalf("re-park = %v, %v; want Parked", res, err)
	}
}

func TestDrain(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// Nothing open: drain returns immediately, and new parks are refused.
	if err := e.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, _, err := e.Park(ctx, ParkRequest{Key: "new"}); err != ErrDraining {
		t.Fatalf("park while draining = %v, want ErrDraining", err)
	}
	// Retransmission replay still answered while draining.
	res, _, err := e.Park(ctx, ParkRequest{Key: "k1"})
	if err != nil || res != ReplayedClosed {
		t.Fatalf("replay while draining = %v, %v; want ReplayedClosed", res, err)
	}
}

func TestDrainWaitsForOpenTickets(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- e.Drain(context.Background()) }()

	select {
	case err := <-done:
		t.Fatalf("drain returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := e.Redeem(ctx, "k1", msg()); err != nil {
		t.Fatalf("redeem during drain: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drain after clear: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("drain did not finish after last ticket cleared")
	}
}

func TestDrainInterrupted(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)

	if _, _, err := e.Park(context.Background(), ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	dctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Drain(dctx) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("interrupted drain returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("drain did not observe cancellation")
	}
}

func TestCloseRefusesFurtherWork(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, nil)
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	e.Close()
	e.Close() // idempotent

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k2"}); err != ErrClosed {
		t.Fatalf("park after close = %v, want ErrClosed", err)
	}
	if _, err := e.Redeem(ctx, "k1", msg()); err != ErrClosed {
		t.Fatalf("redeem after close = %v, want ErrClosed", err)
	}
}

func TestExpiryCallbackPanicRecovered(t *testing.T) {
	clk := newFakeClock()
	e := newTestEngine(t, clk, func(c *Config) {
		c.OnExpire = func(*Ticket) { panic("boom") }
	})
	ctx := context.Background()

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k1"}); err != nil {
		t.Fatalf("park: %v", err)
	}
	clk.Advance(time.Minute) // fires the panicking callback; must not crash

	if _, _, err := e.Park(ctx, ParkRequest{Key: "k2"}); err != nil {
		t.Fatalf("engine unusable after callback panic: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{DefaultTTL: -time.Second}); err == nil {
		t.Fatalf("negative DefaultTTL accepted")
	}
	if _, err := New(Config{DefaultTTL: time.Minute, MaxTTL: time.Second}); err == nil {
		t.Fatalf("MaxTTL below DefaultTTL accepted")
	}
	if _, _, err := mustEngine(t).Park(context.Background(), ParkRequest{}); err == nil {
		t.Fatalf("empty key accepted")
	}
}

func mustEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// Real-clock smoke test: concurrent parks and redeems must be race-clean and
// leave no residue. Retransmission contention on one key must attach exactly
// once.
func TestConcurrentSmoke(t *testing.T) {
	e, err := New(Config{DefaultTTL: 5 * time.Second, RetainAfterClose: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	ctx := context.Background()

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + string(rune('a'+i%26)) + "-" + time.Now().String() + string(rune(i))
			if _, _, err := e.Park(ctx, ParkRequest{Key: key, Destination: "d"}); err != nil {
				t.Errorf("park: %v", err)
				return
			}
			if _, err := e.Redeem(ctx, key, msg()); err != nil {
				t.Errorf("redeem: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if e.InFlight() != 0 {
		t.Fatalf("InFlight = %d after churn, want 0", e.InFlight())
	}

	// Contended retransmission: exactly one Parked, the rest attach.
	var mu sync.Mutex
	counts := map[ParkResult]int{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _, err := e.Park(ctx, ParkRequest{Key: "same", Destination: "d"})
			if err != nil {
				t.Errorf("park same: %v", err)
				return
			}
			mu.Lock()
			counts[res]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[Parked] != 1 || counts[AttachedOpen] != 9 {
		t.Fatalf("contended park counts = %v, want 1 Parked / 9 AttachedOpen", counts)
	}
}
