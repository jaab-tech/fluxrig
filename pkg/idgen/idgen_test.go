// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package idgen

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIDGenerator_FluxID(t *testing.T) {
	machineID := uuid.New()
	gen, err := New(machineID)
	assert.NoError(t, err)

	id, err := gen.NextFluxID()
	assert.NoError(t, err)

	// Verify it's a UUID v7
	assert.Equal(t, uuid.Version(7), id.Version())
}

func TestIDGenerator_EntityID(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-112233445566")
	gen, _ := New(machineID)

	eType := EntityRack
	eid := gen.NewEntityID(eType)

	// Verify Version 7
	assert.Equal(t, uuid.Version(7), eid.Version())

	// Verify EntityType hint (byte 13)
	assert.Equal(t, uint8(eType), eid[13])

	// Verify machine_id hint (bytes 9-12)
	assert.Equal(t, machineID[12], eid[9])
	assert.Equal(t, machineID[13], eid[10])
	assert.Equal(t, machineID[14], eid[11])
	assert.Equal(t, machineID[15], eid[12])
}

func TestIDGenerator_NextEntityID(t *testing.T) {
	gen, _ := New(uuid.New())

	id1 := gen.NextEntityID(EntityGear)
	assert.Equal(t, uint8(EntityGear), id1[13])
}

// Before this fix, a Nil machine ID (a Rack that has not enrolled yet -
// The Zero Config bootstrap, a real and expected state, not a caller
// error) overwrote bytes 9-12 with Nil's own zero bytes, so every
// not-yet-enrolled generator produced the same constant hint there. Two
// separate calls must not land the same bytes in that region now that
// uuid.NewV7's own random bytes are left alone instead.
func TestIDGenerator_NewEntityID_NilMachineIDDoesNotZeroTheHint(t *testing.T) {
	gen, err := New(uuid.Nil)
	require.NoError(t, err, "a Nil machine ID is a legitimate pending-enrollment state, not an error")

	first := gen.NewEntityID(EntityRack)
	second := gen.NewEntityID(EntityRack)

	zero := [4]byte{0, 0, 0, 0}
	assert.NotEqual(t, zero[:], first[9:13], "the hint region must not be clobbered to all-zero by a Nil machine ID")
	assert.NotEqual(t, first[9:13], second[9:13], "two calls on the same Nil-machine-ID generator must not collide on the hint")
}

// A non-Nil machine ID must still work exactly as before.
func TestIDGenerator_NewEntityID_NonNilMachineIDStillHints(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-112233445566")
	gen, err := New(machineID)
	require.NoError(t, err)

	eid := gen.NewEntityID(EntityRack)
	assert.Equal(t, machineID[12], eid[9])
	assert.Equal(t, machineID[13], eid[10])
	assert.Equal(t, machineID[14], eid[11])
	assert.Equal(t, machineID[15], eid[12])
}

// Before this fix, uuid.NewV7's error was discarded (id, _ :=), so an
// entropy-source failure silently produced an entity ID built from a
// zero-value UUID instead of surfacing anything.
func TestIDGenerator_NewEntityID_PanicsWhenNewV7Fails(t *testing.T) {
	gen, err := New(uuid.MustParse("00000000-0000-0000-0000-112233445566"))
	require.NoError(t, err)

	uuid.SetRand(failingReader{})
	defer uuid.SetRand(nil)

	assert.Panics(t, func() {
		gen.NewEntityID(EntityRack)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated entropy source failure")
}

func TestRandomSuffix(t *testing.T) {
	s1, err := RandomSuffix(4)
	require.NoError(t, err)
	assert.Equal(t, 4, len(s1))

	s2, err := RandomSuffix(8)
	require.NoError(t, err)
	assert.Equal(t, 8, len(s2))
	assert.NotEqual(t, s1, s2)

	// Odd and zero lengths must not panic and must honour the length.
	s3, err := RandomSuffix(3)
	require.NoError(t, err)
	assert.Equal(t, 3, len(s3))

	s0, err := RandomSuffix(0)
	require.NoError(t, err)
	assert.Equal(t, "", s0)
}

// Before this fix, RandomSuffix returned the constant "0000" when the entropy
// source failed, so a caller received a plausible, wrong suffix with no signal.
// It must now surface the error instead.
func TestRandomSuffix_EntropyFailureReturnsError(t *testing.T) {
	orig := randRead
	randRead = func([]byte) (int, error) {
		return 0, errors.New("simulated entropy source failure")
	}
	defer func() { randRead = orig }()

	suffix, err := RandomSuffix(4)
	require.Error(t, err)
	assert.Equal(t, "", suffix, "a failed entropy read must not yield a placeholder suffix")
}

// The machine-id hint is the low 32 bits, so two machine IDs that differ only
// above those bits share a hint. They must still produce different entity IDs:
// the hint is for grouping, not an identity.
func TestIDGenerator_NewEntityID_SharedHintStillDistinctIDs(t *testing.T) {
	// Both end in the same 4 bytes (112233445566) and differ above them.
	a := uuid.MustParse("00000000-0000-0000-0000-112233445566")
	b := uuid.MustParse("00000000-0000-0000-0001-112233445566")

	genA, err := New(a)
	require.NoError(t, err)
	genB, err := New(b)
	require.NoError(t, err)

	idA := genA.NewEntityID(EntityRack)
	idB := genB.NewEntityID(EntityRack)

	assert.Equal(t, idA[9:13], idB[9:13], "the two machine IDs share the low-32-bit hint by construction")
	assert.NotEqual(t, idA, idB, "the full IDs must still differ")
}
