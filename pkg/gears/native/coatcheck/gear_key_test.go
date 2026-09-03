// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// keyGear builds a gear with only the fields extractKey reads, so these tests
// exercise key derivation without standing up a bus.
func keyGear(t *testing.T, normalize string, fields ...string) *CoatCheckGear {
	t.Helper()
	return &CoatCheckGear{config: &Config{KeyFields: fields, KeyNormalize: normalize}}
}

func msgWith(pairs map[string]any) *fluxmsg.FluxMsg {
	return &fluxmsg.FluxMsg{Data: pairs, Metadata: map[string]string{}}
}

// TestKeyAgreesAcrossRenderings is the defect this normalization exists for.
//
// A request and its reply can cross gears configured with different specs, so
// the same trace number arrives zero-padded on one side and trimmed on the
// other. The join is exact, so the two produce different keys, the restore
// finds nothing, and nothing reports an error: the correlation simply stops
// happening. Whitespace is the same defect in its most common form.
func TestKeyAgreesAcrossRenderings(t *testing.T) {
	t.Run("whitespace is trimmed by default", func(t *testing.T) {
		g := keyGear(t, NormalizeTrim, "data.stan")

		padded, err := g.extractKey(msgWith(map[string]any{"stan": "  123  "}))
		require.NoError(t, err)
		bare, err := g.extractKey(msgWith(map[string]any{"stan": "123"}))
		require.NoError(t, err)

		assert.Equal(t, bare, padded, "surrounding whitespace must not change the key")
	})

	t.Run("leading zeros collapse only when asked", func(t *testing.T) {
		trim := keyGear(t, NormalizeTrim, "data.stan")
		zeroPadded, err := trim.extractKey(msgWith(map[string]any{"stan": "000123"}))
		require.NoError(t, err)
		bare, err := trim.extractKey(msgWith(map[string]any{"stan": "123"}))
		require.NoError(t, err)
		assert.NotEqual(t, bare, zeroPadded,
			"trim must not silently merge values whose padding may be significant")

		num := keyGear(t, NormalizeNumeric, "data.stan")
		zeroPadded, err = num.extractKey(msgWith(map[string]any{"stan": "000123"}))
		require.NoError(t, err)
		bare, err = num.extractKey(msgWith(map[string]any{"stan": "123"}))
		require.NoError(t, err)
		assert.Equal(t, bare, zeroPadded, "numeric must reconcile differing declared widths")
	})

	t.Run("an all-zero value stays a value", func(t *testing.T) {
		g := keyGear(t, NormalizeNumeric, "data.stan")

		zeros, err := g.extractKey(msgWith(map[string]any{"stan": "000000"}))
		require.NoError(t, err)
		single, err := g.extractKey(msgWith(map[string]any{"stan": "0"}))
		require.NoError(t, err)

		assert.Equal(t, single, zeros)
		assert.NotEmpty(t, zeros, "collapsing to empty would make it look like a missing field")
	})

	t.Run("none preserves the exact rendering", func(t *testing.T) {
		g := keyGear(t, NormalizeNone, "data.stan")

		padded, err := g.extractKey(msgWith(map[string]any{"stan": "  123"}))
		require.NoError(t, err)
		bare, err := g.extractKey(msgWith(map[string]any{"stan": "123"}))
		require.NoError(t, err)

		assert.NotEqual(t, bare, padded, "none is the opt-out and must not normalize")
	})
}

// TestKeySeparatesComposedFields pins that a multi-field key cannot be forged by
// moving characters across the boundary between two fields.
func TestKeySeparatesComposedFields(t *testing.T) {
	g := keyGear(t, NormalizeTrim, "data.term", "data.stan")

	a, err := g.extractKey(msgWith(map[string]any{"term": "12", "stan": "3456"}))
	require.NoError(t, err)
	b, err := g.extractKey(msgWith(map[string]any{"term": "1234", "stan": "56"}))
	require.NoError(t, err)

	assert.NotEqual(t, a, b, "field boundaries must survive the join")
}
