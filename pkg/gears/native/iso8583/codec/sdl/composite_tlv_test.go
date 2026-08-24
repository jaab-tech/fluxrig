// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/field"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emvSDL declares DE 55 as a BER-TLV composite with three EMV tags. Anything
// else arriving inside DE 55 is unknown to the spec and must survive anyway.
const emvSDL = `
meta:
  name: "emv_tlv"
  version: "1.0.0"
fields:
  0:
    label: "MTI"
    type: "numeric"
    length: 4
    enc: "ascii"
  1:
    label: "Bitmap"
    type: "binary"
    length: 8
    enc: "binary"
  55:
    label: "ICC Data"
    type: "lllvar"
    length: 999
    enc: "binary"
    len_enc: "ascii"
    structure: "tlv"
    tlv_tag_encoding: "hex"
    tlv_len_encoding: "binary"
    subfields:
      "9F02":
        label: "Amount, Authorized"
        type: "binary"
        enc: "binary"
      "5F2A":
        label: "Transaction Currency Code"
        type: "binary"
        enc: "binary"
      "9F36":
        label: "Application Transaction Counter"
        type: "binary"
        enc: "binary"
`

func loadEMVSpec(t *testing.T) *iso8583.MessageSpec {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "emv.yaml")
	require.NoError(t, os.WriteFile(path, []byte(emvSDL), 0o600))

	spec, _, err := LoadSpec(path)
	require.NoError(t, err)
	return spec
}

// iccWithUnknownTag builds DE 55 content holding two declared tags and one the
// spec does not model, in BER-TLV form.
func iccWithUnknownTag(t *testing.T) []byte {
	t.Helper()
	// 9F02 06 000000000501   declared
	// 9F1F 04 DEADBEEF       NOT in the spec: private brand data
	// 5F2A 02 0858           declared
	raw, err := hex.DecodeString("9F0206000000000501" + "9F1F04DEADBEEF" + "5F2A020858")
	require.NoError(t, err)
	return raw
}

func TestSDLComposite_IsNativeMoovComposite(t *testing.T) {
	spec := loadEMVSpec(t)

	f, ok := spec.Fields[55]
	require.True(t, ok, "DE 55 must be defined")

	comp, ok := f.(*field.Composite)
	require.True(t, ok, "a structured field must build a native moov Composite")

	tag := comp.Spec().Tag
	require.NotNil(t, tag)
	// Both flags are required: moov nests the store logic inside the skip
	// branch, so StoreUnknownTLVTags alone silently does nothing.
	assert.True(t, tag.SkipUnknownTLVTags, "skip must be enabled for the store branch to run")
	assert.True(t, tag.StoreUnknownTLVTags, "unknown tags must be retained, not dropped")
	assert.NotNil(t, tag.PrefUnknownTLV, "unknown tag lengths need a prefixer")
}

// TestSDLComposite_UnknownTagRoundTrip is the Phase A acceptance test: a tag the
// spec does not model must come back out of the encoder byte for byte.
func TestSDLComposite_UnknownTagRoundTrip(t *testing.T) {
	spec := loadEMVSpec(t)
	icc := iccWithUnknownTag(t)

	msg := iso8583.NewMessage(spec)
	require.NoError(t, msg.Field(0, "0100"))
	require.NoError(t, msg.BinaryField(55, icc))

	packed, err := msg.Pack()
	require.NoError(t, err)

	decoded := iso8583.NewMessage(spec)
	require.NoError(t, decoded.Unpack(packed))

	// The unknown tag is retained rather than discarded.
	unknown := iso8583.UnknownTags(decoded)
	require.Contains(t, unknown, "55.9F1F", "unknown tag must be preserved, got %v", keys(unknown))

	raw, err := unknown["55.9F1F"].Bytes()
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", hex.EncodeToString(raw))

	// And re-encoding reproduces the original bytes, which is what a switch
	// forwarding to the scheme depends on.
	repacked, err := decoded.Pack()
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(packed), hex.EncodeToString(repacked),
		"re-encoded message must be byte-identical to the original")
}

func TestSDLComposite_DeclaredSubfieldsDecode(t *testing.T) {
	spec := loadEMVSpec(t)

	msg := iso8583.NewMessage(spec)
	require.NoError(t, msg.Field(0, "0100"))
	require.NoError(t, msg.BinaryField(55, iccWithUnknownTag(t)))
	packed, err := msg.Pack()
	require.NoError(t, err)

	decoded := iso8583.NewMessage(spec)
	require.NoError(t, decoded.Unpack(packed))

	comp, ok := decoded.GetField(55).(*field.Composite)
	require.True(t, ok)

	subs := comp.GetSubfields()
	require.Contains(t, subs, "9F02")
	require.Contains(t, subs, "5F2A")

	amount, err := subs["9F02"].String()
	require.NoError(t, err)
	assert.Equal(t, "000000000501", amount)
}

// TestSDLComposite_MalformedLengths carries forward the regression coverage
// written for the hand-rolled parser it replaces. A crafted BER-TLV long-form
// length used to wrap to a negative value, slip past a bounds check and crash
// the codec.
//
// The bytes are fed straight to the composite, which is the real attack
// surface: a malformed message arriving from the network. moov guards this
// since v0.26.1 (#416 for the store path, #427 for the skip path), and this
// pins that the SDL configuration actually reaches the guard.
func TestSDLComposite_MalformedLengths(t *testing.T) {
	spec := loadEMVSpec(t)

	tests := []struct {
		name    string
		content string // DE 55 content, hex
	}{
		{"long form overflows to negative", "8A888000000000000000"},
		{"long form exceeds remaining data", "8A887FFFFFFFFFFFFFFF"},
		{"length beyond the data", "8A7F"},
		{"truncated long form", "8A8800"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, err := hex.DecodeString(tc.content)
			require.NoError(t, err)

			// The field carries an ASCII LLL length prefix.
			wire := append([]byte(fmt.Sprintf("%03d", len(content))), content...)

			comp := field.NewComposite(spec.Fields[55].Spec())
			require.NotPanics(t, func() {
				_, err = comp.Unpack(wire)
			}, "malformed TLV must not panic the parser")
			assert.Error(t, err, "malformed TLV must be reported as an error")
		})
	}
}

func keys(m map[string]field.Field) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
