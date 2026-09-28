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

// A passport is entirely self-consistent: it carries its own Mixer public key
// alongside a signature made with the matching private key. That is exactly
// what a forged passport also looks like, so verifyPassport must not accept
// a passport's own claimed key as its own proof; it has to check that key
// against the one already pinned for this Rack the first time a passport was
// ever accepted.
func TestVerifyPassport_PinsOnFirstUseAndRejectsAKeySwapLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.flux")

	realMixer, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	machineID := uuid.New()
	realState := &pki.RackState{MachineID: machineID, Name: "rack-a", Status: "active", MixerPublic: realMixer.Public}
	realEnv, err := realMixer.Sign(realState)
	require.NoError(t, err)

	got, err := verifyPassport(path, realEnv)
	require.NoError(t, err, "the first passport ever seen for this path has nothing to pin against yet")
	assert.Equal(t, realState.Name, got.Name)

	pinned, err := pki.LoadPinnedMixerKey(path)
	require.NoError(t, err)
	assert.Equal(t, realMixer.Public, pinned, "the real Mixer's key must now be the trust anchor for this path")

	// An attacker who can write an envelope also controls a private key: they
	// can sign their own envelope, embedding their own public key exactly the
	// way the real Mixer does. Nothing about the envelope alone is wrong.
	forger, errForger := pki.GenerateClusterKey()
	require.NoError(t, errForger)
	forgedState := &pki.RackState{MachineID: machineID, Name: "rack-a", Status: "active", MixerPublic: forger.Public}
	forgedEnv, errSign := forger.Sign(forgedState)
	require.NoError(t, errSign)

	// The old, unpinned check has no way to tell the forgery apart from the
	// real thing: it verifies against whatever key the payload itself claims.
	_, errUnpinned := forgedEnv.Verify()
	require.NoError(t, errUnpinned, "a self-signed envelope verifies against its own embedded key")

	// verifyPassport must reject it: the forged key does not match the one
	// already pinned for this path.
	_, err = verifyPassport(path, forgedEnv)
	assert.Error(t, err, "a passport signed by a key other than the one pinned for this Rack must not verify")
}

// A legitimate re-issue by the same Mixer (e.g. pending -> active promotion)
// must keep verifying, and must not disturb the pin.
func TestVerifyPassport_AcceptsALaterEnvelopeFromTheSamePinnedMixer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.flux")

	mixer, err := pki.GenerateClusterKey()
	require.NoError(t, err)
	machineID := uuid.New()

	first := &pki.RackState{MachineID: machineID, Name: "rack-a", Status: "pending", MixerPublic: mixer.Public}
	firstEnv, errSign1 := mixer.Sign(first)
	require.NoError(t, errSign1)
	_, err = verifyPassport(path, firstEnv)
	require.NoError(t, err)

	second := &pki.RackState{MachineID: machineID, Name: "rack-a", Status: "active", MixerPublic: mixer.Public}
	secondEnv, errSign2 := mixer.Sign(second)
	require.NoError(t, errSign2)

	got, err := verifyPassport(path, secondEnv)
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status)

	pinned, err := pki.LoadPinnedMixerKey(path)
	require.NoError(t, err)
	assert.Equal(t, mixer.Public, pinned, "the pin must not change across a legitimate re-issue")
}
