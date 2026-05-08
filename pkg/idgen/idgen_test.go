// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package idgen

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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

func TestRandomSuffix(t *testing.T) {
	s1 := RandomSuffix(4)
	assert.Equal(t, 4, len(s1))
	s2 := RandomSuffix(8)
	assert.Equal(t, 8, len(s2))
	assert.NotEqual(t, s1, s2)
}
