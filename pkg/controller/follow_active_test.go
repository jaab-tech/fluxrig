// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

func scenarioYAML(name string) []byte {
	return []byte("meta:\n  name: " + name + "\n  version: \"1.0.0\"\ngears: []\nwires: []\nracks: []\n")
}

// A follower that does not hold the lease converges on the pointer: when the
// holder moves it, FollowActive reloads and the API reports the new scenario.
func TestFollowActive_ConvergesToThePointer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx))

	mixerID := uuid.New()
	gen, err := idgen.New(mixerID)
	require.NoError(t, err)
	sc := NewScenarioController(slog.Default(), store.topo, nil, store, gen, mixerID, time.Second)

	require.NoError(t, store.PutScenario(ctx, "blue", scenarioYAML("blue")))
	require.NoError(t, store.SetActive(ctx, registry.ActiveScenario{Name: "blue", Version: "1.0.0", Content: scenarioYAML("blue")}))
	require.NoError(t, sc.LoadActive(ctx))
	require.Equal(t, "blue", sc.CurrentName())

	// The holder moves the pointer while the follower polls.
	require.NoError(t, store.PutScenario(ctx, "green", scenarioYAML("green")))
	require.NoError(t, store.SetActive(ctx, registry.ActiveScenario{Name: "green", Version: "1.0.0", Content: scenarioYAML("green")}))

	followCtx, stop := context.WithCancel(ctx)
	defer stop()
	go sc.FollowActive(followCtx, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		return sc.CurrentName() == "green"
	}, 5*time.Second, 10*time.Millisecond, "the follower did not converge on the moved pointer")
	require.Equal(t, "1.0.0", sc.CurrentVersion())
}
