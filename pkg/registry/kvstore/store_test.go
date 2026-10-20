// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

const boot = "bootstrap"

func newTestStore(t *testing.T) (*Store, *MemoryBucket, *MemoryBucket) {
	t.Helper()
	reg := NewMemoryBucket(0)
	live := NewMemoryBucket(90 * time.Second)
	s := New(slog.Default(), reg, live, nil, uuid.New(), Config{MaxValueSize: 1024, WriteRetries: 3})
	s.SetBootstrapSecret(boot)
	return s, reg, live
}

func register(t *testing.T, s *Store, name string) *registry.Rack {
	t.Helper()
	r, err := s.Register(context.Background(), uuid.New(), name, boot, "10.0.0.1", 9000, "v1", nil, uuid.New())
	require.NoError(t, err)
	return r
}

// A fresh registration returns its secret once, and the registry keeps only
// its hash: the plaintext appears in no stored value.
func TestRegister_SecretIsReturnedOnceAndStoredHashed(t *testing.T) {
	s, reg, _ := newTestStore(t)
	ctx := context.Background()
	r := register(t, s, "rack-a")
	require.NotEmpty(t, r.Secret)

	keys, err := reg.Keys(ctx, "")
	require.NoError(t, err)
	for _, k := range keys {
		v, _, _ := reg.Get(ctx, k)
		assert.NotContains(t, string(v), r.Secret, "plaintext secret stored under %s", k)
	}

	again, err := s.Get(ctx, r.MachineID)
	require.NoError(t, err)
	assert.Empty(t, again.Secret, "a read never returns the secret")
	assert.True(t, registry.SecretMatches(again.SecretHash, r.Secret))
	assert.False(t, registry.SecretMatches(again.SecretHash, "wrong"))
}

// A known MachineID must present its own secret; the bootstrap secret no
// longer works for it.
func TestRegister_KnownMachineNeedsItsSecret(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	r := register(t, s, "rack-a")

	_, err := s.Register(ctx, r.MachineID, "rack-a", boot, "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, errIdentityMismatch)

	back, err := s.Register(ctx, r.MachineID, "ignored-name", r.Secret, "10.0.0.2", 2, "v2", nil, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, "rack-a", back.Name, "the registered name is kept over the Hello's")
	assert.Equal(t, "10.0.0.2", back.IP)
	assert.Empty(t, back.Secret, "a returning Rack is not sent its secret again")
}

func TestRegister_BootstrapSecretGatesEntry(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Register(context.Background(), uuid.New(), "rack-a", "not-it", "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, registry.ErrBootstrapSecretMismatch)
}

// Concurrent fresh registrations for one name: exactly one wins, every other
// one gets a name conflict. The DuckDB store read, then wrote, with nothing
// in between but a unique index.
func TestRegister_ConcurrentSameNameExactlyOneWins(t *testing.T) {
	s, _, _ := newTestStore(t)
	const n = 20
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Register(context.Background(), uuid.New(), "contested", boot, "ip", 1, "v1", nil, uuid.New())
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, registry.ErrNameConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
	assert.Equal(t, int32(n-1), conflicts.Load())

	racks, err := s.List(context.Background(), "")
	require.NoError(t, err)
	assert.Len(t, racks, 1, "losers leave no record behind")
}

// A Rack that lost its passport but kept its secret returns under a new
// MachineID and takes its record and name with it.
func TestRegister_AdoptionMovesRecordAndName(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	r := register(t, s, "rack-a")
	newID := uuid.New()

	adopted, err := s.Register(ctx, newID, "rack-a", r.Secret, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, newID, adopted.MachineID)

	_, err = s.Get(ctx, r.MachineID)
	require.ErrorIs(t, err, registry.ErrNotFound)
	_, err = s.Approve(ctx, newID, "")
	require.NoError(t, err)
	id, err := s.GetRackByName(ctx, "rack-a")
	require.NoError(t, err)
	assert.Equal(t, newID, id)

	_, err = s.Register(ctx, uuid.New(), "rack-a", "wrong", "ip", 1, "v1", nil, uuid.New())
	require.ErrorIs(t, err, registry.ErrNameConflict)
}

func TestApprove_RenameMovesTheName(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	r := register(t, s, "rack-a")

	got, err := s.Approve(ctx, r.MachineID, "rack-b")
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "rack-b", got.Name)

	_, err = s.GetRackByName(ctx, "rack-a")
	require.ErrorIs(t, err, registry.ErrNotFound, "the old name is released")
	register(t, s, "rack-a") // and can be taken again
}

func TestApprove_RefusedRenameLeavesRackUnchanged(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	a := register(t, s, "rack-a")
	register(t, s, "rack-b")

	_, err := s.Approve(ctx, a.MachineID, "rack-b")
	require.ErrorIs(t, err, registry.ErrNameConflict)
	got, err := s.Get(ctx, a.MachineID)
	require.NoError(t, err)
	assert.Equal(t, "rack-a", got.Name)
	assert.Equal(t, "pending", got.Status)
}

// A name key left behind by an interrupted step (its holder no longer
// carries the name) does not block the name forever.
func TestClaimName_OrphanKeyIsReusable(t *testing.T) {
	s, reg, _ := newTestStore(t)
	ctx := context.Background()
	ghost := nameRef{TypeID: typeRack, MachineID: uuid.New()}
	_, err := reg.Create(ctx, nameKey("rack-a"), encode(ghost))
	require.NoError(t, err)

	r := register(t, s, "rack-a")
	s.SetAutoAdopt(true)
	_, err = s.Approve(ctx, r.MachineID, "")
	require.NoError(t, err)
	id, err := s.GetRackByName(ctx, "rack-a")
	require.NoError(t, err)
	assert.Equal(t, r.MachineID, id)
}

// conflictingBucket makes the next n updates fail with a revision conflict,
// as a concurrent writer would.
type conflictingBucket struct {
	*MemoryBucket
	failures atomic.Int32
}

func (b *conflictingBucket) Update(ctx context.Context, key string, value []byte, rev uint64) (uint64, error) {
	if b.failures.Add(-1) >= 0 {
		return 0, errConflict
	}
	return b.MemoryBucket.Update(ctx, key, value, rev)
}

// A revision conflict is read again and retried; one that outlasts the
// retries fails without writing.
func TestWrites_RetryConflictsThenGiveUp(t *testing.T) {
	ctx := context.Background()
	cb := &conflictingBucket{MemoryBucket: NewMemoryBucket(0)}
	s := New(slog.Default(), cb, NewMemoryBucket(0), nil, uuid.New(), Config{WriteRetries: 3})
	s.SetBootstrapSecret(boot)
	r, err := s.Register(ctx, uuid.New(), "rack-a", boot, "ip", 1, "v1", nil, uuid.New())
	require.NoError(t, err)

	cb.failures.Store(2)
	require.NoError(t, s.UpdateStatus(ctx, r.MachineID, "inactive"), "two conflicts are within three retries")

	cb.failures.Store(10)
	err = s.UpdateStatus(ctx, r.MachineID, "active")
	require.Error(t, err)
	assert.ErrorIs(t, err, errConflict)
	got, _ := s.Get(ctx, r.MachineID)
	assert.Equal(t, "inactive", got.Status, "a failed write changes nothing")
}

// Presence comes from the liveness bucket and expires on its own; the
// administrative status does not change with it.
func TestHeartbeat_PresenceExpiresStatusStays(t *testing.T) {
	s, _, live := newTestStore(t)
	ctx := context.Background()
	clock := time.Now()
	live.SetClock(func() time.Time { return clock })
	s.SetAutoAdopt(true)
	r := register(t, s, "rack-a")

	got, _ := s.Get(ctx, r.MachineID)
	assert.False(t, got.Online, "no heartbeat yet")

	require.NoError(t, s.Heartbeat(ctx, r.MachineID, map[string]any{"cpu": 1.0}, nil))
	got, _ = s.Get(ctx, r.MachineID)
	assert.True(t, got.Online)
	assert.Equal(t, 1.0, got.Stats["cpu"])

	clock = clock.Add(91 * time.Second)
	got, _ = s.Get(ctx, r.MachineID)
	assert.False(t, got.Online, "offline after the liveness TTL")
	assert.Equal(t, "active", got.Status, "presence never changes the status")
}

func TestHeartbeat_UnknownEntityIsNotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	require.ErrorIs(t, s.Heartbeat(context.Background(), uuid.New(), nil, nil), registry.ErrNotFound)
}

func TestRemoveByName_ReleasesTheName(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	register(t, s, "rack-a")
	require.NoError(t, s.RemoveByName(ctx, "rack-a"))
	racks, _ := s.List(ctx, "")
	assert.Empty(t, racks)
	register(t, s, "rack-a")
}

func TestGetRackByName_PendingIsNotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	register(t, s, "rack-a")
	_, err := s.GetRackByName(context.Background(), "rack-a")
	require.ErrorIs(t, err, registry.ErrNotFound)
}

// The Mixer's own records carry no secret and claim no name, so a Rack may
// be called what a Snake is called on every Mixer.
func TestUpsertSelf_NoNameClaim(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	id := uuid.New()
	require.NoError(t, s.UpsertSelf(ctx, typeSnake, id, "embedded-snake", "ip", 4222, "v1", nil))
	require.NoError(t, s.UpsertSelf(ctx, typeSnake, id, "embedded-snake", "ip", 4222, "v2", nil))
	got, err := s.GetEntity(ctx, typeSnake, id)
	require.NoError(t, err)
	assert.Equal(t, "v2", got.Version)
	register(t, s, "embedded-snake")
}

func TestScenarios_StoreListActivate(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetActive(ctx)
	require.ErrorIs(t, err, registry.ErrNotFound)

	require.NoError(t, s.PutScenario(ctx, "payments v1", []byte("meta: {}")))
	got, err := s.GetScenario(ctx, "payments v1")
	require.NoError(t, err)
	assert.Equal(t, "meta: {}", string(got))
	names, err := s.ListScenarios(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"payments v1"}, names)

	require.NoError(t, s.SetActive(ctx, registry.ActiveScenario{Name: "payments v1", Version: "1.0"}))
	require.NoError(t, s.SetActive(ctx, registry.ActiveScenario{Name: "payments v1", Version: "1.1", Content: []byte("gears: []")}))
	active, err := s.GetActive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "payments v1", active.Name)
	assert.Equal(t, "1.1", active.Version)
	assert.Equal(t, "gears: []", string(active.Content))

	err = s.PutScenario(ctx, "huge", []byte(strings.Repeat("x", 1025)))
	require.ErrorIs(t, err, ErrScenarioTooLarge)
}

func TestKeys_ArbitraryNamesAreValidKeys(t *testing.T) {
	for _, name := range []string{"rack a", "rack.a", "rack/a", "ráck*>"} {
		k := nameKey(name)
		assert.Regexp(t, `^[-/_=.a-zA-Z0-9]+$`, k, fmt.Sprintf("name %q", name))
	}
}

// The entity ID carries a hint of its MachineID and its type, as the IDs
// the DuckDB registry issued did.
func TestRegister_EntityIDCarriesMachineHint(t *testing.T) {
	s, _, _ := newTestStore(t)
	r := register(t, s, "rack-a")
	rec, _, err := s.getRecord(context.Background(), typeRack, r.MachineID)
	require.NoError(t, err)
	assert.Equal(t, r.MachineID[12:16], rec.EntityID[9:13])
	assert.Equal(t, typeRack, rec.EntityID[13])
}

// A Hello's config arrives decoded from CBOR, with nested maps keyed by
// interface{} that JSON cannot encode. It must still be stored: the e2e
// suites caught a record written empty because the encode error was
// swallowed, which then failed every read of that Rack.
func TestRegister_CBORShapedConfigIsStored(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	cfg := map[string]any{
		"nested": map[any]any{"k": "v", 7: []any{map[any]any{"deep": 1.5}}},
		"bad":    math.NaN(),
	}
	r, err := s.Register(ctx, uuid.New(), "rack-cbor", boot, "ip", 1, "v1", cfg, uuid.New())
	require.NoError(t, err)

	got, err := s.Get(ctx, r.MachineID)
	require.NoError(t, err, "the record must decode")
	nested, ok := got.Config["nested"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "v", nested["k"])
	assert.Contains(t, nested, "7")
	assert.Nil(t, got.Config["bad"], "NaN becomes null")

	racks, err := s.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, racks, 1)
}

// Heartbeat stats also arrive decoded from CBOR. The API encodes the Rack
// list as JSON, so stats JSON cannot encode would empty the whole list: the
// e2e suites saw /racks answer with an empty body.
func TestHeartbeat_CBORShapedStatsEncodeAsJSON(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	r := register(t, s, "rack-a")
	require.NoError(t, s.Heartbeat(ctx, r.MachineID, map[string]any{"mem": map[any]any{"heap": 1}}, map[string]any{"x": map[any]any{1: 2}}))
	racks, err := s.List(ctx, "")
	require.NoError(t, err)
	_, err = json.Marshal(racks)
	require.NoError(t, err, "the API encodes this list as JSON")
}
