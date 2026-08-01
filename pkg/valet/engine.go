// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Defaults applied by New when the corresponding Config field is zero.
const (
	DefaultTTL              = time.Minute
	DefaultRetainAfterClose = 5 * time.Second
)

// Config configures an Engine. Zero values take the documented defaults.
type Config struct {
	// DefaultTTL is the open-ticket deadline applied when a ParkRequest does
	// not set its own TTL. Zero means DefaultTTL (1m).
	DefaultTTL time.Duration

	// MaxTTL caps every ticket TTL, including per-park overrides. Zero means
	// no cap. When set, it must not be smaller than the effective DefaultTTL.
	MaxTTL time.Duration

	// RetainAfterClose is the window during which a redeemed ticket's outcome
	// is kept for idempotent replay. Zero means DefaultRetainAfterClose (5s);
	// a negative value disables retention entirely.
	RetainAfterClose time.Duration

	// OnExpire is invoked (on a timer goroutine, engine lock not held) for each
	// open ticket whose TTL elapses without redemption. Panics are recovered
	// and logged. May be nil.
	OnExpire func(*Ticket)

	// Store holds open tickets. Nil means an in-memory store.
	Store Store

	// Clock drives deadlines. Nil means the system clock.
	Clock Clock

	// Logger receives engine diagnostics. Nil means slog.Default().
	Logger *slog.Logger
}

// ParkRequest describes one Park call.
type ParkRequest struct {
	// Key is the correlation key. Required.
	Key string

	// Destination labels where the request was routed, for in-flight
	// accounting. Optional.
	Destination string

	// Request is the original message, surfaced again on expiry. Optional but
	// strongly recommended: without it a timeout cannot carry the request to
	// the caller's error handling.
	Request *fluxmsg.FluxMsg

	// ReturnCtx is the caller's return context, handed back on redemption.
	ReturnCtx map[string]string

	// TTL overrides the engine DefaultTTL for this ticket. Zero means default;
	// the value is capped by MaxTTL when configured.
	TTL time.Duration
}

type engineState uint8

const (
	engineRunning engineState = iota
	engineDraining
	engineClosed
)

// Engine is the correlation engine. See the package documentation for the
// guarantees it provides. All methods are safe for concurrent use.
type Engine struct {
	defaultTTL time.Duration
	maxTTL     time.Duration
	retain     time.Duration
	onExpire   func(*Ticket)
	store      Store
	clock      Clock
	log        *slog.Logger

	mu         sync.Mutex
	cond       *sync.Cond
	state      engineState
	openCount  int
	destCounts map[string]int
	retained   map[string]*Ticket
}

// New builds an Engine from cfg, applying defaults for zero values.
func New(cfg Config) (*Engine, error) {
	if cfg.DefaultTTL < 0 {
		return nil, fmt.Errorf("valet: config: DefaultTTL must not be negative")
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = DefaultTTL
	}
	if cfg.MaxTTL < 0 {
		return nil, fmt.Errorf("valet: config: MaxTTL must not be negative")
	}
	if cfg.MaxTTL > 0 && cfg.MaxTTL < cfg.DefaultTTL {
		return nil, fmt.Errorf("valet: config: MaxTTL (%s) is smaller than DefaultTTL (%s)", cfg.MaxTTL, cfg.DefaultTTL)
	}
	switch {
	case cfg.RetainAfterClose == 0:
		cfg.RetainAfterClose = DefaultRetainAfterClose
	case cfg.RetainAfterClose < 0:
		cfg.RetainAfterClose = 0 // retention disabled
	}
	if cfg.Store == nil {
		cfg.Store = NewMemoryStore()
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	e := &Engine{
		defaultTTL: cfg.DefaultTTL,
		maxTTL:     cfg.MaxTTL,
		retain:     cfg.RetainAfterClose,
		onExpire:   cfg.OnExpire,
		store:      cfg.Store,
		clock:      cfg.Clock,
		log:        cfg.Logger,
		destCounts: make(map[string]int),
		retained:   make(map[string]*Ticket),
	}
	e.cond = sync.NewCond(&e.mu)
	return e, nil
}

// Park registers an in-flight request under pr.Key.
//
//   - Parked: a new ticket was created (route the request).
//   - AttachedOpen: the key is already in flight (retransmission); do not
//     route again, the eventual reply answers both attempts.
//   - ReplayedClosed: the key was redeemed within the retention window; the
//     returned ticket's Outcome is the response to replay.
func (e *Engine) Park(ctx context.Context, pr ParkRequest) (ParkResult, *Ticket, error) {
	if pr.Key == "" {
		return Parked, nil, fmt.Errorf("valet: park: empty key")
	}

	ttl := pr.TTL
	if ttl <= 0 {
		ttl = e.defaultTTL
	}
	if e.maxTTL > 0 && ttl > e.maxTTL {
		ttl = e.maxTTL
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.state == engineClosed {
		return Parked, nil, ErrClosed
	}

	// Retransmission of an in-flight request: attach, never double-route.
	if existing, err := e.store.Get(ctx, pr.Key); err == nil {
		return AttachedOpen, existing, nil
	} else if !errors.Is(err, ErrTicketNotFound) {
		return Parked, nil, fmt.Errorf("valet: park: %w", err)
	}

	// Retransmission after redemption: replay the retained outcome.
	if closed, ok := e.retained[pr.Key]; ok {
		return ReplayedClosed, closed, nil
	}

	if e.state == engineDraining {
		return Parked, nil, ErrDraining
	}

	now := e.clock.Now()
	t := &Ticket{
		Key:         pr.Key,
		Destination: pr.Destination,
		ReturnCtx:   pr.ReturnCtx,
		Request:     pr.Request,
		ParkedAt:    now,
		Deadline:    now.Add(ttl),
	}
	t.setState(TicketOpen)

	if existing, err := e.store.Insert(ctx, t); err != nil {
		return Parked, nil, fmt.Errorf("valet: park: %w", err)
	} else if existing != nil {
		// Lost a race we should not lose under the engine lock; treat as attach.
		return AttachedOpen, existing, nil
	}

	e.openCount++
	e.destCounts[t.Destination]++
	t.timer = e.clock.AfterFunc(ttl, func() { e.expire(t) })

	return Parked, t, nil
}

// Redeem matches a reply to its open ticket. The ticket is closed, its outcome
// retained for the retention window, and the ticket returned to the caller
// (return context intact). ErrUnmatched reports a key with no ticket; and
// ErrAlreadyRedeemed a duplicate reply inside the retention window.
func (e *Engine) Redeem(ctx context.Context, key string, outcome *fluxmsg.FluxMsg) (*Ticket, error) {
	if key == "" {
		return nil, fmt.Errorf("valet: redeem: empty key")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.state == engineClosed {
		return nil, ErrClosed
	}

	t, err := e.store.Take(ctx, key)
	if errors.Is(err, ErrTicketNotFound) {
		if _, ok := e.retained[key]; ok {
			return nil, ErrAlreadyRedeemed
		}
		return nil, ErrUnmatched
	}
	if err != nil {
		return nil, fmt.Errorf("valet: redeem: %w", err)
	}

	if t.timer != nil {
		t.timer.Stop()
	}
	t.Outcome = outcome
	t.ClosedAt = e.clock.Now()
	t.setState(TicketClosed)
	e.decOpenLocked(t)

	if e.retain > 0 {
		e.retained[key] = t
		t.timer = e.clock.AfterFunc(e.retain, func() { e.purge(key, t) })
	}

	return t, nil
}

// expire transitions an open ticket to expired when its TTL fires. A ticket
// redeemed between timer fire and lock acquisition is left alone.
func (e *Engine) expire(t *Ticket) {
	e.mu.Lock()
	if e.state == engineClosed {
		e.mu.Unlock()
		return
	}
	cur, err := e.store.Get(context.Background(), t.Key)
	if err != nil || cur != t {
		e.mu.Unlock()
		return
	}
	if _, err := e.store.Take(context.Background(), t.Key); err != nil {
		e.mu.Unlock()
		return
	}
	t.setState(TicketExpired)
	e.decOpenLocked(t)
	cb := e.onExpire
	e.mu.Unlock()

	if cb == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("expiry callback panic recovered", "key", t.Key, "panic", r)
		}
	}()
	cb(t)
}

// purge drops a retained outcome once its retention window ends.
func (e *Engine) purge(key string, t *Ticket) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cur, ok := e.retained[key]; ok && cur == t {
		delete(e.retained, key)
	}
}

// decOpenLocked updates in-flight accounting after a close or expiry.
// Callers hold e.mu.
func (e *Engine) decOpenLocked(t *Ticket) {
	e.openCount--
	if n := e.destCounts[t.Destination]; n <= 1 {
		delete(e.destCounts, t.Destination)
	} else {
		e.destCounts[t.Destination] = n - 1
	}
	e.cond.Broadcast()
}

// InFlight returns the number of open tickets.
func (e *Engine) InFlight() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.openCount
}

// InFlightTo returns the number of open tickets routed to a destination label.
// This is the load signal for least-loaded routing.
func (e *Engine) InFlightTo(dest string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.destCounts[dest]
}

// InFlightSnapshot returns the open-ticket count for each destination in one
// lock acquisition. Least-loaded routing over a K-leaf tree uses this instead
// of K separate InFlightTo calls, so route selection takes the engine lock
// once per request rather than once per leaf.
func (e *Engine) InFlightSnapshot(dests []string) map[string]int {
	out := make(map[string]int, len(dests))
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range dests {
		out[d] = e.destCounts[d]
	}
	return out
}

// Retained returns the number of redeemed outcomes still inside the retention
// window (diagnostic).
func (e *Engine) Retained() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.retained)
}

// Drain refuses new parks but keeps matching replies until every open ticket
// clears (redeemed or expired) or ctx is done, in which case it reports the
// number of tickets still open.
func (e *Engine) Drain(ctx context.Context) error {
	e.mu.Lock()
	if e.state == engineClosed {
		e.mu.Unlock()
		return ErrClosed
	}
	e.state = engineDraining

	// Wake the cond loop when the context ends so we can observe ctx.Err.
	stop := context.AfterFunc(ctx, func() {
		e.mu.Lock()
		e.cond.Broadcast()
		e.mu.Unlock()
	})
	defer stop()

	for e.openCount > 0 && ctx.Err() == nil && e.state == engineDraining {
		e.cond.Wait()
	}
	open := e.openCount
	e.mu.Unlock()

	if open > 0 {
		return fmt.Errorf("valet: drain interrupted with %d open tickets: %w", open, ctx.Err())
	}
	return nil
}

// Close stops all timers and releases all state. Open tickets are dropped
// without their OnExpire callback; callers wanting an orderly stop should
// Drain first. Close is idempotent.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == engineClosed {
		return
	}
	e.state = engineClosed

	open, err := e.store.Purge(context.Background())
	if err != nil {
		e.log.Error("close: purging open tickets failed", "error", err)
	}
	for _, t := range open {
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	for _, t := range e.retained {
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	e.retained = make(map[string]*Ticket)
	e.openCount = 0
	e.destCounts = make(map[string]int)
	e.cond.Broadcast()
}
