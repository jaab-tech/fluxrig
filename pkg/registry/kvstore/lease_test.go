// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// leaseFixture returns two stores sharing one registry bucket and one lease
// bucket, each with its own clock, plus the shared clock controls.
type leaseFixture struct {
	leaseBucket *MemoryBucket
	a, b        *Store
	la, lb      *Lease
	clockA      *time.Time
	clockB      *time.Time
}

func newLeaseFixture(t *testing.T) *leaseFixture {
	t.Helper()
	reg := NewMemoryBucket(0)
	live := NewMemoryBucket(time.Minute)
	leaseBucket := NewMemoryBucket(30 * time.Second)

	clockA := time.Unix(1000, 0).UTC()
	clockB := time.Unix(1000, 0).UTC()
	leaseBucket.SetClock(func() time.Time { return clockA }) // the bucket expires by the holder's clock in this test

	mk := func(id uuid.UUID, clk *time.Time) (*Store, *Lease) {
		s := New(slog.Default(), reg, live, nil, id, Config{MaxValueSize: 1024, WriteRetries: 3})
		s.SetBootstrapSecret(boot)
		// One clock drives both the store and its lease, as one process would.
		clock := clk
		s.now = func() time.Time { return *clock }
		l := NewLease(leaseBucket, id, LeaseConfig{TTL: 30 * time.Second, Renew: 10 * time.Second},
			func() *slog.Logger { return slog.Default() }, nil)
		l.now = func() time.Time { return *clock }
		s.SetLease(l)
		return s, l
	}

	a, la := mk(uuid.New(), &clockA)
	b, lb := mk(uuid.New(), &clockB)
	return &leaseFixture{leaseBucket: leaseBucket, a: a, b: b, la: la, lb: lb, clockA: &clockA, clockB: &clockB}
}

// Only one Mixer holds the lease; the other's control writes are refused.
func TestLease_OnlyOneHolderAndTheOtherIsRefused(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()

	gotA, err := f.la.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, gotA, "the first acquire takes a free lease")

	gotB, err := f.lb.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, gotB, "the second cannot take a held lease")
	assert.False(t, f.lb.Held())

	// The holder writes; the non-holder is refused.
	_, err = f.a.Register(ctx, uuid.New(), "rack-a", boot, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err, "the holder registers")
	_, err = f.b.Register(ctx, uuid.New(), "rack-b", boot, "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, ErrNotLeaseHolder, "a non-holder's control write is refused")
}

// The holder stops renewing; only after the TTL does the other take over, and
// not before. The old holder's write is refused once its lease lapses.
func TestLease_TakeoverOnlyAfterTTL(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()

	require.True(t, func() bool { ok, _ := f.la.Acquire(ctx); return ok }())
	// The holder renews once, then goes silent.
	_, err := f.la.Renew(ctx)
	require.NoError(t, err)

	// Just before the TTL, B still cannot take it.
	advance(f.clockA, 29*time.Second)
	advance(f.clockB, 29*time.Second)
	got, err := f.lb.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, got, "a lease is not stealable before its TTL")

	// After the TTL the holder's own view says lost, and B takes over.
	advance(f.clockA, 2*time.Second)
	advance(f.clockB, 2*time.Second)
	assert.False(t, f.la.Held(), "a holder that has not renewed within TTL considers the lease lost")

	got, err = f.lb.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, got, "the lease is free after its TTL and B takes it")

	// The old holder's write is now refused; the new holder's succeeds.
	_, err = f.a.Register(ctx, uuid.New(), "rack-old", boot, "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, ErrNotLeaseHolder, "the paused old holder cannot write")
	_, err = f.b.Register(ctx, uuid.New(), "rack-new", boot, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err, "the new holder writes")
}

// A take-over is a revision-checked update, so a paused holder that resumes
// cannot overwrite the new holder's value.
func TestLease_PausedHolderCannotStealBack(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()
	require.True(t, func() bool { ok, _ := f.la.Acquire(ctx); return ok }())

	advance(f.clockA, 31*time.Second)
	advance(f.clockB, 31*time.Second)
	ok, err := f.lb.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, ok)

	// A's stale revision no longer matches, so its renew fails and it stays out.
	held, err := f.la.Renew(ctx)
	require.NoError(t, err)
	assert.False(t, held, "the paused holder cannot renew over the new holder")
}

// With no lease attached, a single Mixer writes exactly as before.
func TestLease_AbsentLeaseAllowsWrites(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Register(context.Background(), uuid.New(), "solo", boot, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err, "a store with no lease writes freely")
	assert.NoError(t, s.requireLease())
}

// The error names the condition so the API can map it to 503.
func TestLease_ErrorIsNamed(t *testing.T) {
	assert.True(t, strings.Contains(ErrNotLeaseHolder.Error(), "lease"),
		"the error text names the lease so it is recognizable in logs")
	_ = registry.ErrNotFound // keep the registry import used if assertions change
}

func advance(t *time.Time, d time.Duration) { *t = t.Add(d) }

// A holder that restarts finds its own key, which may be about to expire.
// It renews the key before it trusts it, so no other Mixer can take the
// lease while the restarted one still believes it holds it.
func TestLease_RestartedHolderRenewsItsOwnKey(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()
	require.True(t, func() bool { ok, _ := f.la.Acquire(ctx); return ok }())
	firstRev := f.la.revision

	// A restarts 25s later with the same identity.
	advance(f.clockA, 25*time.Second)
	advance(f.clockB, 25*time.Second)
	restarted := NewLease(f.leaseBucket, f.la.mixerID, LeaseConfig{TTL: 30 * time.Second, Renew: 10 * time.Second}, nil, nil)
	restarted.now = func() time.Time { return *f.clockA }
	ok, err := restarted.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, firstRev, restarted.revision, "the found key was renewed, not just trusted")

	// 31s after the first write the original key would have expired. The
	// renewed one has not, so B cannot take the lease.
	advance(f.clockA, 6*time.Second)
	advance(f.clockB, 6*time.Second)
	got, err := f.lb.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, got, "B cannot take a lease the restarted holder renewed")
	assert.True(t, restarted.Held())
}

// slowBucket advances a clock while a write is in flight, as a slow reply
// would.
type slowBucket struct {
	*MemoryBucket
	clock *time.Time
	delay time.Duration
}

func (b *slowBucket) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	rev, err := b.MemoryBucket.Create(ctx, key, value)
	advance(b.clock, b.delay)
	return rev, err
}

func (b *slowBucket) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	rev, err := b.MemoryBucket.Update(ctx, key, value, revision)
	advance(b.clock, b.delay)
	return rev, err
}

// The server starts the key's max age when it stores the write. The holder
// trusts its lease from when it sent the request, not from when the reply
// came back, so its view never outlasts the server's.
func TestLease_TrustStartsWhenTheRequestIsSent(t *testing.T) {
	clock := time.Unix(1000, 0)
	mem := NewMemoryBucket(30 * time.Second)
	mem.SetClock(func() time.Time { return clock })
	b := &slowBucket{MemoryBucket: mem, clock: &clock, delay: 2 * time.Second}
	l := NewLease(b, uuid.New(), LeaseConfig{TTL: 30 * time.Second, Renew: 10 * time.Second}, nil, nil)
	l.now = func() time.Time { return clock }

	ok, err := l.Acquire(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	// The reply came 2s after the write; the key expires 30s after the write.
	advance(&clock, 28*time.Second) // 30s after the write
	assert.False(t, l.Held(), "the lease is not trusted past the server's expiry")
}

// Any Mixer reads who holds the lease and since when. The time the holder
// took it survives its renews and a restart, and a new holder starts its own.
func TestLease_HolderAndSince(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()

	_, ok, err := f.lb.Holder(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "no holder before any acquire")

	took := *f.clockA
	require.True(t, func() bool { got, _ := f.la.Acquire(ctx); return got }())
	advance(f.clockA, 10*time.Second)
	advance(f.clockB, 10*time.Second)
	_, err = f.la.Renew(ctx)
	require.NoError(t, err)

	h, ok, err := f.lb.Holder(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, f.la.MixerID(), h.MixerID, "the other Mixer reads the holder")
	assert.True(t, h.Since.Equal(took), "since is when the lease was taken, not the last renew")
	assert.True(t, h.Renewed.Equal(*f.clockA), "renewed is the last renew")

	// The holder restarts with the same identity: it keeps its since.
	restarted := NewLease(f.leaseBucket, f.la.mixerID, LeaseConfig{TTL: 30 * time.Second, Renew: 10 * time.Second}, nil, nil)
	restarted.now = func() time.Time { return *f.clockA }
	got, err := restarted.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, got)
	h, _, err = f.lb.Holder(ctx)
	require.NoError(t, err)
	assert.True(t, h.Since.Equal(took), "a restarted holder keeps the time it took the lease")

	// It goes silent; after the TTL the other Mixer takes over, with its own since.
	advance(f.clockA, 31*time.Second)
	advance(f.clockB, 31*time.Second)
	got, err = f.lb.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, got)
	h, _, err = f.la.Holder(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.lb.MixerID(), h.MixerID)
	assert.True(t, h.Since.Equal(*f.clockB), "a new holder records when it took the lease")
}

// A Mixer that does not hold the lease records its own entities, and is
// still refused a control write.
func TestLease_ANonHolderRecordsItself(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := context.Background()
	got, err := f.la.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, got)

	idB := uuid.New()
	require.NoError(t, f.b.UpsertSelf(ctx, typeMixer, idB, "mixer-b", "ip", 8090, "v1", nil), "a non-holder records itself")
	_, err = f.a.Get(ctx, idB)
	require.Error(t, err, "a Mixer is not a Rack: Get reads Racks")
	require.NoError(t, f.b.HeartbeatEntity(ctx, typeMixer, idB, nil, nil), "its heartbeat finds the entity")

	_, err = f.b.Register(ctx, uuid.New(), "rack-b", boot, "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, ErrNotLeaseHolder, "a control write still needs the lease")
}
