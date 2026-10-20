// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// The reference spec packs the bitmap the way ISO 8583:1987 lays it out: a
// primary bitmap of 16 hex characters, and a secondary one only when a field
// above 64 is present, announced by bit 1. The expected messages are written
// out by hand from that layout.
func TestReferenceSpecPrimaryBitmap(t *testing.T) {
	spec := "../../../../../../../examples/specs/iso8583-v87-ascii.yaml"
	enc := initGear(t, spec, map[string]any{"direction": "encode", "on_error": "kill"})
	dec := initGear(t, spec, map[string]any{"direction": "decode", "on_error": "kill"})

	cases := []struct {
		name string
		data map[string]string
		mti  string
		want string
	}{
		{
			name: "no field above 64: primary bitmap only, bit 1 off",
			mti:  "0200",
			data: map[string]string{"iso8583.field.3": "000000", "iso8583.field.11": "000042"},
			// Bits 3 and 11: 0x20 0x20.
			want: "0200" + "2020000000000000" + "000000" + "000042",
		},
		{
			name: "field 70: bit 1 on, secondary bitmap follows",
			mti:  "0800",
			data: map[string]string{"iso8583.field.7": "1019153000", "iso8583.field.11": "000001", "iso8583.field.70": "301"},
			// Bits 1, 7 and 11: 0x82 0x20. Bit 70 is bit 6 of the secondary: 0x04.
			want: "0800" + "8220000000000000" + "0400000000000000" + "1019153000" + "000001" + "301",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := fluxmsg.New()
			m.Metadata["iso8583.mti"] = c.mti
			for k, v := range c.data {
				require.NoError(t, m.Set(k, v))
			}
			out, err := enc.Process(context.Background(), m)
			require.NoError(t, err)
			assert.Equal(t, c.want, string(out.RawPayload))

			in := fluxmsg.New()
			in.RawPayload = []byte(c.want)
			d, err := dec.Process(context.Background(), in)
			require.NoError(t, err, "a standard message decodes")
			for k := range c.data {
				_, ok := d.Get(k)
				assert.True(t, ok, "%s decoded", k)
			}
		})
	}
}
