// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"encoding/hex"
	"testing"

	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompositeField_Fixed(t *testing.T) {
	spec := &field.Spec{
		Length:      10,
		Description: "Fixed Composite",
		Pref:        prefix.ASCII.Fixed,
	}
	cfg := CompositeConfig{Structure: "fixed"}
	comp := NewCompositeField(spec, cfg)

	// Add subfields: 4 chars, 6 chars
	f1 := field.NewString(&field.Spec{Length: 4, Pref: prefix.ASCII.Fixed, Enc: encoding.ASCII})
	f2 := field.NewString(&field.Spec{Length: 6, Pref: prefix.ASCII.Fixed, Enc: encoding.ASCII})

	comp.AddSubfield("f1", f1)
	comp.AddSubfield("f2", f2)

	// Unpack
	data := []byte("AAAABBBBBB")
	read, err := comp.Unpack(data)
	require.NoError(t, err)
	assert.Equal(t, 10, read)
	assert.Equal(t, "AAAA", comp.values["f1"])
	assert.Equal(t, "BBBBBB", comp.values["f2"])

	// Pack
	packed, err := comp.Pack()
	require.NoError(t, err)
	assert.Equal(t, data, packed)
}

func TestCompositeField_TLV_Hex(t *testing.T) {
	spec := &field.Spec{
		Length:      99,
		Description: "TLV Composite",
		Pref:        prefix.ASCII.LL,
	}
	cfg := CompositeConfig{
		Structure: "tlv",
		TagEnc:    "hex",
		LenEnc:    "binary",
	}
	comp := NewCompositeField(spec, cfg)

	// Add subfields
	// Tag 9F01 (2 bytes because 0x9F & 0x1F == 0x1F)
	f1 := field.NewString(&field.Spec{Length: 4, Pref: prefix.ASCII.Fixed})
	// Tag 4F (1 byte)
	f2 := field.NewString(&field.Spec{Length: 2, Pref: prefix.ASCII.Fixed})

	comp.AddSubfield("9f01", f1)
	comp.AddSubfield("4f", f2)

	// Data:
	// Tag 9F01, Len 04, Val "DATA"
	// Tag 4F, Len 02, Val "HI"
	// 9F01 04 44415441 4F 02 4849
	hexData := "9f0104444154414f024849"
	data, _ := hex.DecodeString(hexData)

	// Note: Unpack expects prefix if spec defined.
	// But let's test the inner logic.
	err := comp.unpackSubfields(data)
	require.NoError(t, err)

	assert.Equal(t, "DATA", comp.values["9f01"])
	assert.Equal(t, "HI", comp.values["4f"])

	// Pack (ordered keys matter)
	packed, err := comp.Pack()
	require.NoError(t, err)
	// Pack includes LL prefix "11" (11 bytes total content)
	assert.Equal(t, "11"+string(data), string(packed))
}

func TestCompositeField_TLV_Ascii(t *testing.T) {
	spec := &field.Spec{
		Length:      99,
		Description: "TLV Composite",
		Pref:        prefix.ASCII.Fixed, // No prefix for this test call
	}
	spec.Pref = nil // Force no prefix for Pack()

	cfg := CompositeConfig{
		Structure: "tlv",
		TagEnc:    "ascii",
		LenEnc:    "ascii",
	}
	comp := NewCompositeField(spec, cfg)

	f1 := field.NewString(&field.Spec{Length: 5, Pref: prefix.ASCII.Fixed})
	comp.AddSubfield("T1", f1)

	// T1 05 HELLO
	data := []byte("T105HELLO")
	err := comp.unpackSubfields(data)
	require.NoError(t, err)
	assert.Equal(t, "HELLO", comp.values["T1"])

	packed, err := comp.Pack()
	require.NoError(t, err)
	assert.Equal(t, data, packed)
}
func TestCompositeField_Fixed_InsufficientData(t *testing.T) {
	spec := &field.Spec{Length: 10, Pref: prefix.ASCII.Fixed}
	comp := NewCompositeField(spec, CompositeConfig{Structure: "fixed"})
	comp.AddSubfield("f1", field.NewString(&field.Spec{Length: 10, Pref: prefix.ASCII.Fixed}))

	data := []byte("SHORT")
	_, err := comp.Unpack(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not enough data")
}

func TestCompositeField_TLV_UnknownTag(t *testing.T) {
	comp := NewCompositeField(&field.Spec{}, CompositeConfig{
		Structure: "tlv",
		TagEnc:    "hex",
		LenEnc:    "binary",
	})

	// Data with TAG 5E (unknown), Len 02, Val "XX" followed by TAG 4F (known)
	data, _ := hex.DecodeString("5e0258584f024849")

	f2 := field.NewString(&field.Spec{Length: 2, Pref: prefix.ASCII.Fixed})
	comp.AddSubfield("4f", f2)

	err := comp.unpackSubfields(data)
	assert.NoError(t, err)
	assert.Equal(t, "HI", comp.values["4f"])
	assert.Empty(t, comp.values["5e"])
}

func TestCompositeField_Unmarshal_InvalidType(t *testing.T) {
	comp := NewCompositeField(&field.Spec{}, CompositeConfig{})
	var i int
	err := comp.Unmarshal(&i)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestCompositeField_SyncState_Prefixed(t *testing.T) {
	spec := &field.Spec{Length: 10, Pref: prefix.ASCII.LL}
	comp := NewCompositeField(spec, CompositeConfig{Structure: "fixed"})
	comp.AddSubfield("f1", field.NewString(&field.Spec{Length: 10, Pref: prefix.ASCII.Fixed}))

	// Data is LL prefix "10" + 10 bytes content
	data := []byte("10AAAABBBBBB")
	err := comp.SetBytes(data)
	assert.NoError(t, err)
	assert.Equal(t, "AAAABBBBBB", comp.values["f1"])
}
