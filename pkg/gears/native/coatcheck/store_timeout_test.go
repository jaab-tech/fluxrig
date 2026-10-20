// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// stalledKV never completes a write until the caller's context ends, like a
// control plane that stopped answering.
type stalledKV struct{ bus.KeyValue }

func (stalledKV) PutWithTTL(ctx context.Context, _, _ string, _ []byte, _ time.Duration) (uint64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type stalledBus struct{ *bus.MockBus }

func (b stalledBus) KV() bus.KeyValue { return stalledKV{b.MockBus.KV()} }

// The awaited write is bounded by store_timeout, like the detached one: a
// store that does not answer fails the message instead of holding it.
func TestAwaitedStoreIsBoundedByStoreTimeout(t *testing.T) {
	g := New().(*CoatCheckGear)
	require.NoError(t, g.Init(&mockGearContext{
		bus: stalledBus{bus.NewMockBus()},
		config: map[string]any{
			"mode":          ModeStore,
			"bucket":        "TEST_STALLED",
			"key_fields":    []string{"meta.id"},
			"value_fields":  []string{"meta.data"},
			"store_timeout": "50ms",
		},
	}))
	require.NoError(t, g.Start(context.Background(), func(*fluxmsg.FluxMsg) {}))

	msg := fluxmsg.New()
	msg.Metadata["id"] = "abc"
	msg.Metadata["data"] = "secret"

	done := make(chan error, 1)
	go func() {
		_, err := g.Process(context.Background(), msg)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(10 * time.Second):
		t.Fatal("the awaited store write was not bounded by store_timeout")
	}
}
