// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
)

func carryRuntime(t *testing.T, id uuid.UUID, name string) *rt.Manager {
	t.Helper()
	gen, _ := idgen.New(id)
	return rt.NewManager(id, name, bus.NewNatsBus("flux-msg"), gen, nil, time.Second, time.Second, 10*time.Millisecond, false, false, nil)
}

func TestSessionCarry_TakeAdoptsTheRuntimeOfTheSameRack(t *testing.T) {
	id := uuid.New()
	manager := carryRuntime(t, id, "rack-a")
	scenarios := &scenarioSession{}
	c := &sessionCarry{runtime: manager, scenarios: scenarios, machineID: id, name: "rack-a"}

	got, adopted := c.take(id, "rack-a", slog.Default())

	require.True(t, adopted)
	assert.Same(t, manager, got.runtime)
	assert.Same(t, scenarios, got.scenarios)
	assert.Nil(t, c.runtime, "what was taken is no longer there")
}

// A Rack whose identity changed is not the one that started the gears, so they stop.
func TestSessionCarry_TakeDiscardsARuntimeOfAnotherIdentity(t *testing.T) {
	id := uuid.New()
	c := &sessionCarry{runtime: carryRuntime(t, id, "rack-a"), machineID: id, name: "rack-a"}

	got, adopted := c.take(uuid.New(), "rack-a", slog.Default())
	assert.False(t, adopted)
	assert.Nil(t, got.runtime)

	c2 := &sessionCarry{runtime: carryRuntime(t, id, "rack-a"), machineID: id, name: "rack-a"}
	_, adopted = c2.take(id, "renamed", slog.Default())
	assert.False(t, adopted)
}

func TestSessionCarry_NothingToTake(t *testing.T) {
	c := &sessionCarry{}

	got, adopted := c.take(uuid.New(), "rack-a", slog.Default())

	assert.False(t, adopted)
	assert.Nil(t, got.runtime)
}

func TestMixerPublicKey(t *testing.T) {
	cluster, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	state := &pki.RackState{MachineID: uuid.New(), Name: "rack-a", Status: "active", MixerPublic: cluster.Public}
	env, err := cluster.Sign(state)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "state.flux")
	require.NoError(t, env.Save(path))

	assert.Equal(t, []byte(cluster.Public), mixerPublicKey(path))
	assert.Nil(t, mixerPublicKey(filepath.Join(t.TempDir(), "missing.flux")))
}
