// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"testing"

	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/padding"
	"github.com/moov-io/iso8583/prefix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// A fixed-length field decodes at the width the wire carried. The library
// strips padding on unpack, so without the restore DE 11 "000042" decoded as
// "42", an all-zero DE 3 as "0", and the MTI in Data as "200". A
// variable-length field keeps the value the sender sent.
func TestDecodeKeepsFixedWidth(t *testing.T) {
	spec := "../../../../../../../examples/specs/iso8583-v87-ascii.yaml"
	enc := initGear(t, spec, map[string]any{"direction": "encode", "on_error": "kill"})
	dec := initGear(t, spec, map[string]any{"direction": "decode", "on_error": "kill"})

	m := fluxmsg.New()
	m.Metadata["iso8583.mti"] = "0200"
	m.Data["proc_code"] = "000000"
	m.Data["stan"] = "000042"
	m.Data["pan"] = "0012345678"
	out, err := enc.Process(context.Background(), m)
	require.NoError(t, err)

	in := fluxmsg.New()
	in.RawPayload = out.RawPayload
	d, err := dec.Process(context.Background(), in)
	require.NoError(t, err)

	for key, want := range map[string]string{
		"mti":              "0200",
		"proc_code":        "000000",
		"stan":             "000042",
		"iso8583.field.3":  "000000",
		"iso8583.field.11": "000042",
		"pan":              "0012345678",
	} {
		got, ok := d.Get(key)
		require.True(t, ok, "%s present", key)
		assert.Equal(t, want, got, key)
	}

	// The restored values re-encode to the same bytes.
	again, err := enc.Process(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, string(out.RawPayload), string(again.RawPayload), "round trip")
}

// Only text fields get their width back. A Binary field renders its bytes as
// hex, two characters per byte, while its Length counts bytes, so padding the
// rendering would change the bytes a re-encode produces.
func TestFixedWidthTextFieldsOnly(t *testing.T) {
	pad := padding.NewLeftPadder('0')
	str := field.NewString(&field.Spec{Length: 6, Enc: encoding.ASCII, Pref: prefix.ASCII.Fixed, Pad: pad})
	assert.Equal(t, "000042", fixedWidth(str, "42"), "a fixed String field is padded to its length")

	bin := field.NewBinary(&field.Spec{Length: 4, Enc: encoding.Binary, Pref: prefix.Binary.Fixed, Pad: pad})
	assert.Equal(t, "0B", fixedWidth(bin, "0B"), "a Binary field is left as unpacked")

	llvar := field.NewString(&field.Spec{Length: 19, Enc: encoding.ASCII, Pref: prefix.ASCII.LL, Pad: pad})
	assert.Equal(t, "42", fixedWidth(llvar, "42"), "a variable-length field is left as unpacked")
}
