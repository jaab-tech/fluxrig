// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func samplePayload(machineID uuid.UUID, rack string) *fluxmsg.ScenarioPayload {
	return &fluxmsg.ScenarioPayload{
		Version:   "1.2.0",
		Name:      "payment-flow",
		RackName:  rack,
		MachineID: machineID,
		Scenario:  []byte("meta:\n  name: payment-flow\n"),
		Timestamp: 1789000000,
		Specs:     []fluxmsg.SpecArtifact{{Name: "visa", Tag: "v1.0.0", Content: []byte("big spec")}},
	}
}

func TestLocalScenarioStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "scenario.flux")
	store := NewLocalScenarioStore(path)
	id := uuid.New()

	require.NoError(t, store.Save(samplePayload(id, "rack-1")))

	got, err := store.Load(id, "rack-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "1.2.0", got.Version)
	assert.Equal(t, "payment-flow", got.Name)
	assert.Equal(t, []byte("meta:\n  name: payment-flow\n"), got.Scenario)
	assert.Empty(t, got.Specs, "spec artefacts live in the spec store, not in this copy")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLocalScenarioStore_NothingSaved(t *testing.T) {
	store := NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))

	got, err := store.Load(uuid.New(), "rack-1")
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestLocalScenarioStore_SaveReplaces(t *testing.T) {
	store := NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))
	id := uuid.New()

	first := samplePayload(id, "rack-1")
	require.NoError(t, store.Save(first))
	second := samplePayload(id, "rack-1")
	second.Version = "2.0.0"
	require.NoError(t, store.Save(second))

	got, err := store.Load(id, "rack-1")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", got.Version)

	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file may be left behind")
}

// A copy projected for another Rack is never resumed: state left over from a
// different enrollment must not start someone else's gears.
func TestLocalScenarioStore_RefusesAnotherRacksScenario(t *testing.T) {
	store := NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))
	id := uuid.New()
	require.NoError(t, store.Save(samplePayload(id, "rack-1")))

	_, err := store.Load(uuid.New(), "rack-1")
	assert.ErrorIs(t, err, ErrLocalScenarioMismatch, "same name, other machine")

	_, err = store.Load(id, "rack-2")
	assert.ErrorIs(t, err, ErrLocalScenarioMismatch, "same machine, other name")
}

func TestLocalScenarioStore_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.flux")
	require.NoError(t, os.WriteFile(path, []byte("not cbor at all"), 0o600))

	got, err := NewLocalScenarioStore(path).Load(uuid.New(), "rack-1")
	assert.Error(t, err)
	assert.Nil(t, got)
	assert.NotErrorIs(t, err, ErrLocalScenarioMismatch)
}

func TestLocalScenarioStore_SaveRejectsEmpty(t *testing.T) {
	store := NewLocalScenarioStore(filepath.Join(t.TempDir(), "scenario.flux"))

	assert.Error(t, store.Save(nil))
	assert.Error(t, store.Save(&fluxmsg.ScenarioPayload{Name: "x"}))
}
