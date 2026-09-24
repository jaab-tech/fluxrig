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

// TestKeyResolvesNestedDataPaths pins that dotted data paths descend nested
// maps, including the map[any]any shape CBOR yields after a bus hop. The
// conductor reads the same vocabulary through msg.Get; coatcheck must agree.
func TestKeyResolvesNestedDataPaths(t *testing.T) {
	nested := map[string]any{
		"iso8583": map[string]any{
			"field": map[string]any{"3": "000000", "4": "10000"},
		},
	}
	g := keyGear(t, NormalizeTrim, "data.iso8583.field.3", "data.iso8583.field.4")
	key, err := g.extractKey(msgWith(nested))
	require.NoError(t, err)
	assert.NotEmpty(t, key)

	// Same content through a CBOR-shaped map must produce the same key.
	cborShaped := map[string]any{
		"iso8583": map[any]any{
			"field": map[any]any{"3": "000000", "4": "10000"},
		},
	}
	key2, err := g.extractKey(msgWith(cborShaped))
	require.NoError(t, err)
	assert.Equal(t, key, key2, "map shape must not change the key")
}

// TestDeepMergeDataKeepsSiblings pins that restoring one nested leaf does
// not clobber the sibling leaves the live message already holds.
func TestDeepMergeDataKeepsSiblings(t *testing.T) {
	dst := map[string]any{
		"iso8583": map[string]any{
			"field": map[string]any{"3": "000000", "4": "10000"},
		},
	}
	src := map[string]any{
		"iso8583": map[string]any{
			"field": map[string]any{"2": "4111111111111111"},
		},
	}
	deepMergeData(dst, src, false)
	fields := dst["iso8583"].(map[string]any)["field"].(map[string]any)
	assert.Equal(t, "4111111111111111", fields["2"])
	assert.Equal(t, "000000", fields["3"], "sibling must survive the merge")
	assert.Equal(t, "10000", fields["4"], "sibling must survive the merge")

	// Preserve mode keeps a live leaf over a restored one.
	dst2 := map[string]any{"a": "live"}
	deepMergeData(dst2, map[string]any{"a": "saved", "b": "saved"}, false)
	assert.Equal(t, "live", dst2["a"])
	assert.Equal(t, "saved", dst2["b"])

	// Overwrite mode replaces.
	deepMergeData(dst2, map[string]any{"a": "saved"}, true)
	assert.Equal(t, "saved", dst2["a"])
}

// TestDeepMergeDataWritesBackACBORShapedNestedMap pins that restoring into a
// live message whose nested Data arrived as map[any]any (a CBOR round trip,
// the shape a message actually carries after crossing the bus) reaches dst,
// not just a disposable copy asDataMap builds along the way.
func TestDeepMergeDataWritesBackACBORShapedNestedMap(t *testing.T) {
	dst := map[string]any{
		"iso8583": map[any]any{
			"field": map[any]any{"3": "000000", "4": "10000"},
		},
	}
	src := map[string]any{
		"iso8583": map[string]any{
			"field": map[string]any{"2": "4111111111111111"},
		},
	}
	deepMergeData(dst, src, false)

	fields := dst["iso8583"].(map[string]any)["field"].(map[string]any)
	assert.Equal(t, "4111111111111111", fields["2"], "the restored field must reach dst, not a disconnected copy")
	assert.Equal(t, "000000", fields["3"], "sibling must survive the merge")
	assert.Equal(t, "10000", fields["4"], "sibling must survive the merge")
}

// TestDeleteDottedStripsOnlyTheConfiguredPath pins the shielding half
// of store: value fields leave the forwarded message, key fields and
// siblings stay.
func TestDeleteDottedStripsOnlyTheConfiguredPath(t *testing.T) {
	msg := msgWith(map[string]any{
		"iso8583": map[string]any{
			"field": map[string]any{"2": "4111111111111111", "3": "000000", "4": "100"},
		},
	})
	msg.Metadata["conn.id"] = "c1"

	deleteDotted(msg, "data.iso8583.field.2")
	deleteDotted(msg, "meta.conn.id")

	fields := msg.Data["iso8583"].(map[string]any)["field"].(map[string]any)
	_, hasPAN := fields["2"]
	assert.False(t, hasPAN, "stored PAN must leave the forwarded message")
	assert.Equal(t, "000000", fields["3"], "sibling must stay")
	assert.Equal(t, "100", fields["4"], "sibling must stay")
	_, hasConn := msg.Metadata["conn.id"]
	assert.False(t, hasConn, "stored meta must leave too")

	// Unknown paths are no-ops, never errors.
	deleteDotted(msg, "data.iso8583.field.99")
	deleteDotted(msg, "meta.nope")
	deleteDotted(nil, "data.x")
}

// The map[any]any shape (a CBOR round trip, the shape a message actually
// carries after crossing the bus) must strip the same way map[string]any
// does: deleteNested only had test coverage for the in-process shape before
// this, on both sides of the fluxmsg.AsDataMap consolidation.
func TestDeleteDottedStripsACBORShapedNestedMap(t *testing.T) {
	msg := msgWith(map[string]any{
		"iso8583": map[any]any{
			"field": map[any]any{"2": "4111111111111111", "3": "000000", "4": "100"},
		},
	})

	deleteDotted(msg, "data.iso8583.field.2")

	fields := msg.Data["iso8583"].(map[string]any)["field"].(map[string]any)
	_, hasPAN := fields["2"]
	assert.False(t, hasPAN, "stored PAN must leave the forwarded message")
	assert.Equal(t, "000000", fields["3"], "sibling must stay")
	assert.Equal(t, "100", fields["4"], "sibling must stay")
}
