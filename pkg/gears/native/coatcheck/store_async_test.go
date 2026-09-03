// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// TestAwaitStoreDefaultsToBlocking pins the safe default.
//
// The original use of this gear strips a field and reattaches it on the reply,
// which only works if the entry exists by the time the reply arrives. Silently
// flipping that to fire-and-forget would produce replies that can never be made
// whole, so the non-blocking path has to be asked for.
func TestAwaitStoreDefaultsToBlocking(t *testing.T) {
	g := &CoatCheckGear{}
	require.NoError(t, g.Init(sdk.NewMockGearContext(map[string]any{
		"mode": ModeStore, "bucket": "b", "key_fields": []string{"meta.k"},
	})))
	assert.True(t, g.config.AwaitStore, "a store must wait unless told otherwise")
	assert.Equal(t, 5*time.Second, g.config.StoreTimeout)
}

// TestAwaitStoreCanBeTurnedOff covers the case this option exists for: an entry
// parked only to measure something, where holding an authorization for a
// control-plane write would trade a payment for a metric.
func TestAwaitStoreCanBeTurnedOff(t *testing.T) {
	g := &CoatCheckGear{}
	require.NoError(t, g.Init(sdk.NewMockGearContext(map[string]any{
		"mode": ModeStore, "bucket": "b", "key_fields": []string{"meta.k"},
		"await_store": false, "store_timeout": "250ms",
	})))
	assert.False(t, g.config.AwaitStore)
	assert.Equal(t, 250*time.Millisecond, g.config.StoreTimeout)
}
