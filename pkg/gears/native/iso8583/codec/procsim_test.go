// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/padding"
	"github.com/moov-io/iso8583/prefix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// switchSpecPath points at the payment-switch validation SDL, relative to this
// package directory.
func switchSpecPath() string {
	return filepath.Join("..", "..", "..", "..", "..",
		"test", "robot", "suites", "payment_switch", "specs", "switch.yaml")
}

// toolSpec independently replicates the wire format the fluxrig ISO 8583 load
// tool speaks (cmd/iso8583-tool getEchoSpec, ASCII mode): MTI + primary bitmap
// + fields 7, 11, 70, ASCII, numeric fields left-padded with '0'. It is built
// here from moov primitives, NOT from the SDL, so that a successful cross-decode
// proves the SDL spec byte-matches the tool rather than merely matching itself.
func toolSpec() *iso8583.MessageSpec {
	enc := encoding.ASCII
	return &iso8583.MessageSpec{
		Name: "ToolEcho",
		Fields: map[int]field.Field{
			0:  field.NewString(&field.Spec{Length: 4, Description: "MTI", Enc: enc, Pref: prefix.None.Fixed}),
			1:  field.NewBitmap(&field.Spec{Description: "Bitmap", Enc: encoding.Binary, Pref: prefix.None.Fixed}),
			7:  field.NewString(&field.Spec{Length: 10, Description: "Transmission Date & Time", Enc: enc, Pref: prefix.None.Fixed, Pad: padding.NewLeftPadder('0')}),
			11: field.NewString(&field.Spec{Length: 6, Description: "STAN", Enc: enc, Pref: prefix.None.Fixed, Pad: padding.NewLeftPadder('0')}),
			70: field.NewString(&field.Spec{Length: 3, Description: "Network Management Information Code", Enc: enc, Pref: prefix.None.Fixed, Pad: padding.NewLeftPadder('0')}),
		},
	}
}

func newCodec(t *testing.T, direction string) *Gear {
	t.Helper()
	g := &Gear{}
	require.NoError(t, g.Init(&mockGearContext{
		config: map[string]any{"spec_path": switchSpecPath(), "direction": direction, "on_error": "reject"},
		logger: slog.Default(),
	}))
	return g
}

// TestProcessorSimAlignment is the scouting-spike proof: a terminal request in
// the load tool's exact wire format decodes through the payment-switch SDL, a
// processor-simulator authors a response (flip MTI to 0810, set DE39=00, echo
// DE41) as a bento gear would, and the re-encoded reply decodes back with the
// correlation field preserved. This confronts the one flagged risk, that the
// SDL spec must byte-match the tool, without needing a Mixer or Rack.
func TestProcessorSimAlignment(t *testing.T) {
	ctx := context.Background()

	// 1. Terminal builds a 0800 request with the TOOL's spec (STAN 1 -> "000001").
	req := iso8583.NewMessage(toolSpec())
	require.NoError(t, req.Field(0, "0800"))
	require.NoError(t, req.Field(7, "0102150405"))
	require.NoError(t, req.Field(11, "1"))
	require.NoError(t, req.Field(70, "301"))
	reqBytes, err := req.Pack()
	require.NoError(t, err)

	// 2. Processor-sim DECODES the wire bytes with the switch SDL spec.
	dec := newCodec(t, "decode")
	in := fluxmsg.New()
	in.RawPayload = reqBytes
	decoded, err := dec.Process(ctx, in)
	require.NoError(t, err, "SDL spec must decode the tool's wire format")

	assert.Equal(t, "0800", decoded.Metadata["iso8583.mti"], "MTI decoded")
	stan, ok := decoded.Get("stan")
	require.True(t, ok, "STAN alias present")
	assert.Equal(t, "1", stan, "STAN decoded (moov unpads leading zeros; consistent on both legs)")
	net, _ := decoded.Get("network_code")
	assert.Equal(t, "301", net, "DE70 decoded")

	// 3. Processor-sim authors the response, exactly as the bento response
	//    builder would (root/meta over Data/Metadata): flip MTI, set DE39=00,
	//    echo a terminal id in DE41. Correlation fields (7, 11, 70) are kept.
	decoded.Metadata["iso8583.mti"] = "0810"
	require.NoError(t, decoded.Set("iso8583.field.39", "00"))
	require.NoError(t, decoded.Set("iso8583.field.41", "TERM0001"))

	// 4. ENCODE the response with the switch SDL spec.
	enc := newCodec(t, "encode")
	encoded, err := enc.Process(ctx, decoded)
	require.NoError(t, err, "authored response must encode")
	require.NotEmpty(t, encoded.RawPayload, "response bytes produced")

	// 5. Decode the reply back (switch spec) and assert it is a well-formed
	//    0810 approval that still carries the request's STAN for correlation.
	back := newCodec(t, "decode")
	replyIn := fluxmsg.New()
	replyIn.RawPayload = encoded.RawPayload
	reply, err := back.Process(ctx, replyIn)
	require.NoError(t, err)

	assert.Equal(t, "0810", reply.Metadata["iso8583.mti"], "response MTI")
	rc, ok := reply.Get("response_code")
	require.True(t, ok, "DE39 present in the reply")
	assert.Equal(t, "00", rc, "approved")
	replyStan, _ := reply.Get("stan")
	assert.Equal(t, "1", replyStan, "STAN preserved for correlation (same unpadded value on the reply)")
	term, _ := reply.Get("terminal_id")
	assert.Equal(t, "TERM0001", term, "DE41 authored by the simulator")
}
