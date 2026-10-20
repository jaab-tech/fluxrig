// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

const grace = 5 * time.Minute

// sweepStore returns a store whose clock the test moves.
func sweepStore(t *testing.T) (*Store, *MemoryBucket, *time.Time) {
	t.Helper()
	s, reg, _ := newTestStore(t)
	clock := time.Now().UTC()
	s.now = func() time.Time { return clock }
	return s, reg, &clock
}

// putRecord writes a Rack record directly, as a crashed write sequence
// would have left it.
func putRecord(t *testing.T, reg *MemoryBucket, id uuid.UUID, name string, at time.Time) {
	t.Helper()
	_, err := reg.Create(context.Background(), entityKey(typeRack, id), encode(&record{
		TypeID: typeRack, MachineID: id, Name: name, Status: "active", FirstSeen: at, UpdatedAt: at,
	}))
	require.NoError(t, err)
}

func putName(t *testing.T, reg *MemoryBucket, name string, id uuid.UUID) {
	t.Helper()
	_, err := reg.Create(context.Background(), nameKey(name), encode(nameRef{TypeID: typeRack, MachineID: id}))
	require.NoError(t, err)
}

// A registration that stopped after writing its record and before claiming
// its name left a record no name points at. Its secret never reached a
// Rack: the sweep deletes it once it is older than the grace period.
func TestSweep_RemovesAnUnfinishedRegistration(t *testing.T) {
	s, reg, clock := sweepStore(t)
	ctx := context.Background()
	id := uuid.New()
	putRecord(t, reg, id, "rack-a", *clock)

	res, err := s.SweepOrphans(ctx, grace)
	require.NoError(t, err)
	assert.Zero(t, res.Removed, "inside the grace period it may still be in progress")

	*clock = clock.Add(grace + time.Second)
	res, err = s.SweepOrphans(ctx, grace)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed)
	_, err = s.Get(ctx, id)
	require.ErrorIs(t, err, registry.ErrNotFound)
}

// A rename that stopped after updating the record and before claiming the
// new name: the old name still points at the Rack. The sweep finishes it.
func TestSweep_CompletesAnInterruptedRename(t *testing.T) {
	s, reg, clock := sweepStore(t)
	ctx := context.Background()
	id := uuid.New()
	putRecord(t, reg, id, "rack-new", *clock)
	putName(t, reg, "rack-old", id)
	*clock = clock.Add(grace + time.Second)

	res, err := s.SweepOrphans(ctx, grace)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Repaired)
	got, err := s.GetRackByName(ctx, "rack-new")
	require.NoError(t, err)
	assert.Equal(t, id, got)
	_, _, err = reg.Get(ctx, nameKey("rack-old"))
	require.ErrorIs(t, err, errKeyNotFound, "the old name is released")
}

// If another Rack took the new name meanwhile, the rename rolls back to
// the name the Rack still holds.
func TestSweep_RollsBackARenameWhoseNameIsTaken(t *testing.T) {
	s, reg, clock := sweepStore(t)
	ctx := context.Background()
	id, other := uuid.New(), uuid.New()
	putRecord(t, reg, id, "rack-new", *clock)
	putName(t, reg, "rack-old", id)
	putRecord(t, reg, other, "rack-new", *clock)
	putName(t, reg, "rack-new", other)
	*clock = clock.Add(grace + time.Second)

	res, err := s.SweepOrphans(ctx, grace)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Repaired)
	got, err := s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "rack-old", got.Name)
	holder, err := s.GetRackByName(ctx, "rack-new")
	require.NoError(t, err)
	assert.Equal(t, other, holder, "the other Rack keeps its name")
}

// A removal that deleted the record and stopped before releasing the name
// left a key pointing at nothing.
func TestSweep_RemovesANameKeyWhoseEntityIsGone(t *testing.T) {
	s, reg, _ := sweepStore(t)
	putName(t, reg, "ghost", uuid.New())

	res, err := s.SweepOrphans(context.Background(), grace)
	require.NoError(t, err)
	assert.Equal(t, 1, res.NameKeysRemoved)
	_, _, err = reg.Get(context.Background(), nameKey("ghost"))
	require.ErrorIs(t, err, errKeyNotFound)
}

// A healthy registry is left as it is, and the Mixer's own records, which
// claim no name by design, are not orphans.
func TestSweep_LeavesHealthyEntitiesAlone(t *testing.T) {
	s, _, clock := sweepStore(t)
	ctx := context.Background()
	s.SetAutoAdopt(true)
	r := register(t, s, "rack-a")
	require.NoError(t, s.UpsertSelf(ctx, typeSnake, uuid.New(), "embedded-snake", "ip", 4222, "v1", nil))
	*clock = clock.Add(grace + time.Second)

	res, err := s.SweepOrphans(ctx, grace)
	require.NoError(t, err)
	assert.Equal(t, SweepResult{}, res)
	_, err = s.Get(ctx, r.MachineID)
	require.NoError(t, err)
}

// A failure to mint an ID is an error, never a record with the nil ID: two
// such records would collide.
func TestRegister_IDFailureIsAnError(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.newUUID = func() (uuid.UUID, error) { return uuid.Nil, errors.New("no entropy") }

	_, err := s.Register(context.Background(), uuid.Nil, "rack-a", boot, "ip", 1, "v1", nil, uuid.New())
	require.Error(t, err)
	_, err = s.Register(context.Background(), uuid.New(), "rack-b", boot, "ip", 1, "v1", nil, uuid.New())
	require.Error(t, err, "the entity ID needs a UUID too")
	require.Error(t, s.UpsertSelf(context.Background(), typeMixer, uuid.New(), "m", "ip", 1, "v1", nil))

	racks, err := s.List(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, racks, "nothing is written")
}

// flakyNames fails every read of a name key, as a JetStream leader election
// during a sweep would.
type flakyNames struct{ *MemoryBucket }

func (f flakyNames) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	if len(key) > 5 && key[:5] == "name." {
		return nil, 0, errors.New("nats: timeout")
	}
	return f.MemoryBucket.Get(ctx, key)
}

// A name key that cannot be read stops the sweep. Skipping it made the
// healthy Rack it points at look unnamed, and the record pass deleted it.
func TestSweep_StopsWhenANameKeyCannotBeRead(t *testing.T) {
	reg := NewMemoryBucket(0)
	s := New(slog.Default(), flakyNames{reg}, NewMemoryBucket(90*time.Second), nil, uuid.New(), Config{MaxValueSize: 1024, WriteRetries: 3})
	clock := time.Now().UTC()
	s.now = func() time.Time { return clock }
	ctx := context.Background()

	id := uuid.New()
	putRecord(t, reg, id, "rack-a", clock)
	putName(t, reg, "rack-a", id)
	clock = clock.Add(grace + time.Second)

	res, err := s.SweepOrphans(ctx, grace)
	require.Error(t, err, "the sweep must not act on an incomplete view of the names")
	assert.Zero(t, res.Removed)
	_, _, err = reg.Get(ctx, entityKey(typeRack, id))
	require.NoError(t, err, "the healthy Rack is still registered")
}
