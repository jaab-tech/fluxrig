// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// leaseKey is the single key in the lease bucket. The value names the Mixer
// that holds it.
const leaseKey = "holder"

// ErrNotLeaseHolder is returned when a control write is attempted on a Mixer
// that does not hold the write lease. It is the registry sentinel, so the
// forwarder maps it with errors.Is.
var ErrNotLeaseHolder = registry.ErrNotLeaseHolder

// leaseValue is what the lease key holds. Acquired is rewritten on every
// renew; Since stays from the write that took the lease, so an operator can
// read how long the holder has held it. A value written without Since reads
// as zero.
type leaseValue struct {
	MixerID  uuid.UUID `json:"mixer_id"`
	Acquired time.Time `json:"acquired"`
	Since    time.Time `json:"since,omitempty"`
}

// LeaseHolder is what the lease key says about its holder.
type LeaseHolder struct {
	// MixerID names the Mixer that holds the lease.
	MixerID uuid.UUID
	// Since is when that Mixer took the lease. Zero when the holder did not
	// record it.
	Since time.Time
	// Renewed is the holder's last successful renew.
	Renewed time.Time
}

// Lease is the write lease over one key in a replicated bucket: one Mixer
// holds it and does every control-plane write, so two Mixers never write the
// registry at once. The bucket's own max age expires a crashed holder's key,
// so a takeover needs no cooperation from the holder. Held() answers whether
// this Mixer currently holds it; Renew() keeps it, and a renew that finds the
// key gone or taken means this Mixer has lost it and must stop writing.
//
// The lease is a liveness hint, not the correctness fence: every registry
// write is still revision-checked, so a holder that pauses past its lease and
// resumes cannot overwrite a newer write. This only bounds how long the
// cluster refuses new control writes after the holder dies.
type Lease struct {
	bucket   Bucket
	mixerID  uuid.UUID
	ttl      time.Duration
	renew    time.Duration
	log      func() *slog.Logger
	now      func() time.Time
	reporter func(event string)

	mu       sync.Mutex
	revision uint64 // revision of the held key, 0 when not held
	held     bool
	since    time.Time // when this Mixer took the lease, kept across renews
	lastGood time.Time // last successful renew/acquire
}

// LeaseConfig configures a Lease.
type LeaseConfig struct {
	// TTL is the bucket max age and how long a holder may go without a
	// successful renew before it considers the lease lost.
	TTL time.Duration
	// Renew is how often the holder renews. It must be well under TTL so an
	// ordinary slow write does not lapse the lease.
	Renew time.Duration
}

// NewLease returns a lease this Mixer can try to acquire, over one key in
// bucket.
func NewLease(bucket Bucket, mixerID uuid.UUID, cfg LeaseConfig, log func() *slog.Logger, reporter func(event string)) *Lease {
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Second
	}
	if cfg.Renew <= 0 {
		cfg.Renew = 10 * time.Second
	}
	if log == nil {
		log = func() *slog.Logger { return slog.Default() }
	}
	return &Lease{
		bucket:  bucket,
		mixerID: mixerID,
		ttl:     cfg.TTL,
		renew:   cfg.Renew,
		log:     log,
		// time.Now keeps the monotonic reading, so a wall-clock step (NTP,
		// an operator) cannot stretch or shrink how long Held trusts a
		// renew. UTC would strip it.
		now:      time.Now,
		reporter: reporter,
	}
}

// TTL is the lease duration, exposed for the holder's renew loop.
func (l *Lease) TTL() time.Duration { return l.ttl }

// RenewInterval is how often the holder should renew.
func (l *Lease) RenewInterval() time.Duration { return l.renew }

// leaseOpBound caps how long one lease bucket operation may block. The
// callers pass their own context (often the app context, which has no
// deadline), and a bucket op without one waits on the JetStream default
// timeout instead of the lease's own rhythm: a Renew stuck past TTL would
// keep writing after the lease lapsed. Every bucket op below runs under
// the smaller of the caller's deadline and this bound; signatures stay the
// same, the bound is internal.
func (l *Lease) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	bound := l.renew / 2
	if bound < time.Second {
		bound = time.Second
	}
	if bound > 10*time.Second {
		bound = 10 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= bound {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, bound)
}

// Acquire tries to take the lease. It succeeds when the key is absent (a
// create), or when this Mixer already holds it: then the key is renewed with
// a revision-checked update before it is trusted. It fails when another Mixer
// holds it and the key has not expired.
func (l *Lease) Acquire(ctx context.Context) (bool, error) {
	return l.acquire(ctx, true)
}

// acquire is Acquire with one retry allowed when the key expires between the
// create and the read.
func (l *Lease) acquire(ctx context.Context, retry bool) (bool, error) {
	// Every bucket op below runs under the lease's own bound, not the
	// caller's (often deadline-free) context: see bound.
	ctx, cancel := l.bound(ctx)
	defer cancel()
	// The server starts the key's max age when it stores the write, which is
	// after this moment. Trusting the lease from here, not from the reply,
	// keeps the local view inside the server's.
	sent := l.now()
	val, err := json.Marshal(leaseValue{MixerID: l.mixerID, Acquired: sent.UTC(), Since: sent.UTC()})
	if err != nil {
		return false, fmt.Errorf("kvstore: lease: encode: %w", err)
	}
	rev, err := l.bucket.Create(ctx, leaseKey, val)
	if err == nil {
		l.setSince(sent.UTC())
		l.markHeld(rev, sent)
		l.report("acquired")
		l.log().Info("write lease acquired", "mixer", l.mixerID)
		return true, nil
	}
	if !errors.Is(err, errConflict) {
		return false, fmt.Errorf("kvstore: lease: create: %w", err)
	}
	// The key exists: is it ours, or a live holder's?
	raw, cur, errGet := l.bucket.Get(ctx, leaseKey)
	if errors.Is(errGet, errKeyNotFound) {
		if !retry {
			l.markLost()
			return false, nil
		}
		// Expired between Create and Get: a fresh race, retried once.
		return l.acquire(ctx, false)
	}
	if errGet != nil {
		return false, fmt.Errorf("kvstore: lease: read: %w", errGet)
	}
	var held leaseValue
	if json.Unmarshal(raw, &held) != nil || held.MixerID != l.mixerID {
		l.markLost()
		return false, nil
	}
	// Our own key, from before a restart or a lapse. It may have been written
	// up to one TTL ago and be about to expire, so trusting it as it is would
	// let this Mixer write after another one takes over. Renew it first. It
	// keeps the time the key says this Mixer took the lease.
	since := held.Since
	sent = l.now()
	if since.IsZero() {
		since = sent.UTC()
	}
	val, err = json.Marshal(leaseValue{MixerID: l.mixerID, Acquired: sent.UTC(), Since: since})
	if err != nil {
		return false, fmt.Errorf("kvstore: lease: encode: %w", err)
	}
	newRev, errUpd := l.bucket.Update(ctx, leaseKey, val, cur)
	if errUpd == nil {
		l.setSince(since)
		l.markHeld(newRev, sent)
		l.report("acquired")
		l.log().Info("write lease re-acquired", "mixer", l.mixerID)
		return true, nil
	}
	if errors.Is(errUpd, errConflict) || errors.Is(errUpd, errKeyNotFound) {
		l.markLost()
		return false, nil
	}
	return false, fmt.Errorf("kvstore: lease: renew own key: %w", errUpd)
}

// Renew extends the lease with a revision-checked update. It returns false
// when the lease is no longer held by this Mixer, which is the signal to stop
// writing at once.
func (l *Lease) Renew(ctx context.Context) (bool, error) {
	l.mu.Lock()
	rev, held := l.revision, l.held
	l.mu.Unlock()
	if !held {
		// Not held: try to take it, in case it is free.
		return l.Acquire(ctx)
	}
	ctx, cancel := l.bound(ctx)
	defer cancel()
	l.mu.Lock()
	since := l.since
	l.mu.Unlock()
	sent := l.now()
	val, err := json.Marshal(leaseValue{MixerID: l.mixerID, Acquired: sent.UTC(), Since: since})
	if err != nil {
		return false, fmt.Errorf("kvstore: lease: encode: %w", err)
	}
	newRev, err := l.bucket.Update(ctx, leaseKey, val, rev)
	if err == nil {
		l.markHeld(newRev, sent)
		return true, nil
	}
	if errors.Is(err, errConflict) || errors.Is(err, errKeyNotFound) {
		// The key moved or expired under us: we no longer hold it.
		l.markLost()
		l.report("renew_failed")
		l.log().Warn("write lease lost", "mixer", l.mixerID, "error", err)
		return false, nil
	}
	return false, fmt.Errorf("kvstore: lease: renew: %w", err)
}

// Held reports whether this Mixer currently holds the lease. A holder that
// has not renewed within TTL is treated as not held even before a renew
// notices, so a paused holder stops writing as soon as its own clock says so.
func (l *Lease) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return false
	}
	if l.now().Sub(l.lastGood) >= l.ttl {
		l.held = false
		return false
	}
	return true
}

// Release gives up the lease, so a clean shutdown hands it over without
// waiting out the TTL. Best effort: a lost race is not an error.
func (l *Lease) Release(ctx context.Context) {
	l.mu.Lock()
	rev, held := l.revision, l.held
	l.held = false
	l.revision = 0
	l.mu.Unlock()
	if held && rev != 0 {
		_ = l.bucket.Delete(ctx, leaseKey, rev)
		l.report("released")
		l.log().Info("write lease released", "mixer", l.mixerID)
	}
}

// Holder reads which Mixer holds the lease, from the key every Mixer reads.
// ok is false when no Mixer holds it. A read can be served by a replica that
// is behind, so right after a handover it can name the previous holder for a
// moment.
func (l *Lease) Holder(ctx context.Context) (holder LeaseHolder, ok bool, err error) {
	ctx, cancel := l.bound(ctx)
	defer cancel()
	raw, _, err := l.bucket.Get(ctx, leaseKey)
	if errors.Is(err, errKeyNotFound) {
		return LeaseHolder{}, false, nil
	}
	if err != nil {
		return LeaseHolder{}, false, fmt.Errorf("kvstore: lease: read: %w", err)
	}
	var v leaseValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return LeaseHolder{}, false, fmt.Errorf("kvstore: lease: decode: %w", err)
	}
	return LeaseHolder{MixerID: v.MixerID, Since: v.Since, Renewed: v.Acquired}, true, nil
}

// MixerID names this Mixer, the one that tries to hold the lease.
func (l *Lease) MixerID() uuid.UUID { return l.mixerID }

func (l *Lease) setSince(t time.Time) {
	l.mu.Lock()
	l.since = t
	l.mu.Unlock()
}

// markHeld records a successful write. sent is when the request left this
// Mixer: the lease is trusted for one TTL from then, never from the reply.
func (l *Lease) markHeld(rev uint64, sent time.Time) {
	l.mu.Lock()
	l.revision = rev
	l.held = true
	l.lastGood = sent
	l.mu.Unlock()
}

func (l *Lease) markLost() {
	l.mu.Lock()
	l.held = false
	l.revision = 0
	l.mu.Unlock()
}

func (l *Lease) report(event string) {
	if l.reporter != nil {
		l.reporter(event)
	}
}

// String is for logs and tests.
func (l *Lease) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("lease(mixer=%s held=%t rev=%d)", l.mixerID, l.held, l.revision)
}
