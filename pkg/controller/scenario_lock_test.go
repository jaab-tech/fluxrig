// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// blockingScenarioBus parks the fan-out until released, as a slow bus does.
type blockingScenarioBus struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingScenarioBus) Publish(ctx context.Context, _ string, _ *fluxmsg.FluxMsg) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Activate must not hold the controller lock through the fan-out: the push
// publishes with bus timeouts while an enrollment-triggered push needs the
// same lock. While the push is still in flight, a reader must get an answer.
func TestActivate_ReleasesTheLockDuringThePush(t *testing.T) {
	ctx := context.Background()
	store, err := newFixtureStore(slog.Default(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx))
	store.SetAutoAdopt(true)
	store.SetBootstrapSecret("sec")

	mixerID := uuid.New()
	gen, _ := idgen.New(mixerID)
	sc := NewScenarioController(slog.Default(), store.topo, store, store, gen, mixerID, time.Second)
	bus := &blockingScenarioBus{entered: make(chan struct{}), release: make(chan struct{})}
	sc.SetBus(bus)

	_, err = store.Register(ctx, uuid.New(), "rack-1", "sec", "ip", 80, "v1", nil, mixerID)
	require.NoError(t, err)
	name, err := sc.Import(ctx, []byte(`
meta:
  name: slow-push
  version: "1.0.0"
gears:
  - name: "gear-1"
    type: "io_tcp"
    deploy: "rack-1"
`), false)
	require.NoError(t, err)

	activated := make(chan error, 1)
	go func() { activated <- sc.Activate(ctx, name) }()

	select {
	case <-bus.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the push never started")
	}

	// The activation is committed and the push is in flight: a reader must
	// not wait for the bus.
	read := make(chan string, 1)
	go func() { read <- sc.CurrentName() }()
	select {
	case got := <-read:
		require.Equal(t, "slow-push", got)
	case <-time.After(10 * time.Second):
		t.Fatal("a reader waited out the whole fan-out: Activate still holds the lock through the push")
	}

	close(bus.release)
	select {
	case err := <-activated:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Activate did not return after the bus was released")
	}
}
