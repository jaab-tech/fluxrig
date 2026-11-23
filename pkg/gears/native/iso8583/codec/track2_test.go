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

// A Track 2 that ends right after the service code, with no discretionary
// data, encodes and decodes. ISO/IEC 7813 allows it. The library refused it
// with "invalid track data" before moov-io/iso8583 v0.26.4.
func TestTrack2WithoutDiscretionaryData(t *testing.T) {
	spec := "../../../../../../../examples/specs/iso8583-v87-ascii.yaml"
	enc := initGear(t, spec, map[string]any{"direction": "encode", "on_error": "kill"})
	dec := initGear(t, spec, map[string]any{"direction": "decode", "on_error": "kill"})

	const track = "4111111111111111=2812101"
	m := fluxmsg.New()
	m.Metadata["iso8583.mti"] = "0100"
	m.Data["track2"] = track
	out, err := enc.Process(context.Background(), m)
	require.NoError(t, err)

	in := fluxmsg.New()
	in.RawPayload = out.RawPayload
	d, err := dec.Process(context.Background(), in)
	require.NoError(t, err)
	got, ok := d.Get("track2")
	require.True(t, ok, "track2 present")
	assert.Equal(t, track, got)

	again, err := enc.Process(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, string(out.RawPayload), string(again.RawPayload), "round trip")
}
