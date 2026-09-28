// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
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

// TestStore_PartialCloneKeepsTheTTLOverride is a regression test for a real
// bug: the lightweight clone value_fields builds copied only FluxID and
// TSInit from the original message, so a per-message TTL override
// (fluxmsg.MetaCoatCheckTTL) in Metadata survived into the stored blob only
// if a scenario happened to also list it in value_fields, which nothing
// documents and no scenario configuring value_fields for an unrelated reason
// would think to do.
func TestStore_PartialCloneKeepsTheTTLOverride(t *testing.T) {
	mockBus := bus.NewMockBus()
	g := New().(*CoatCheckGear)
	require.NoError(t, g.Init(&mockGearContext{
		bus: mockBus,
		config: map[string]any{
			"mode":         ModeStore,
			"bucket":       "TEST_TTL_CLONE",
			"key_fields":   []string{"meta.id"},
			"value_fields": []string{"meta.data"}, // deliberately does not name coatcheck.ttl
		},
	}))
	require.NoError(t, g.Start(context.Background(), func(*fluxmsg.FluxMsg) {}))

	msg := fluxmsg.New()
	msg.Metadata["id"] = "abc"
	msg.Metadata["data"] = "secret"
	msg.Metadata[fluxmsg.MetaCoatCheckTTL] = "30s"

	_, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	// The key is base64 of JoinKeys("abc"), the netstring-length-prefixed form
	// ("3:abc"), not "abc" itself.
	raw, _, err := mockBus.KV().Get(context.Background(), "TEST_TTL_CLONE", "MzphYmM")
	require.NoError(t, err)
	require.NotNil(t, raw, "the entry must have been stored")

	var stored fluxmsg.FluxMsg
	require.NoError(t, cbor.Unmarshal(raw, &stored))
	assert.Equal(t, "30s", stored.Metadata[fluxmsg.MetaCoatCheckTTL],
		"the TTL override must survive into the partial clone without being named in value_fields")
}
