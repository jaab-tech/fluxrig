// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// syncBus delivers to its handler on the publishing goroutine.
//
// MockBus delivers on a goroutine of its own, which makes the race below win
// sometimes and lose sometimes -- it lost on a loaded CI runner and passed two
// hundred times on a quiet laptop. Delivering synchronously makes the losing
// interleaving the only one, so the test either holds or does not.
type syncBus struct {
	bus.MockBus
	handler bus.Handler
}

func (b *syncBus) Subscribe(_ string, h bus.Handler) (bus.Subscription, error) {
	b.handler = h
	return noopSubscription{}, nil
}

func (b *syncBus) Publish(ctx context.Context, _ string, msg *fluxmsg.FluxMsg) error {
	if b.handler != nil {
		b.handler(ctx, msg)
	}
	return nil
}

type noopSubscription struct{}

func (noopSubscription) Unsubscribe() error { return nil }

// Convergence observed after the caller gave up is not convergence.
//
// The bus delivers to its handlers without consulting the caller's context, so
// a probe can land after cancellation. Both the convergence channel and
// ctx.Done() are then ready, and Go picks between ready cases at random -- so
// half the time a cancelled wait reported success. Whoever cancelled is shutting
// down, and telling them the telemetry plane is ready sends them on.
func TestACancelledWaitNeverReportsConvergence(t *testing.T) {
	b := &syncBus{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Repeated because the defect was a coin flip: one run proves nothing.
	for i := 0; i < 50; i++ {
		err := VerifyConnectivity(ctx, b, "test", 10*time.Millisecond, 10*time.Millisecond)
		require.ErrorIs(t, err, context.Canceled, "run %d reported convergence on a cancelled context", i)
	}
}

// The other half of the contract: a probe that comes back on a live context is
// convergence, and still reports it.
func TestALiveWaitStillReportsConvergence(t *testing.T) {
	b := &syncBus{}
	err := VerifyConnectivity(context.Background(), b, "test", time.Second, 10*time.Millisecond)
	require.NoError(t, err)
}
