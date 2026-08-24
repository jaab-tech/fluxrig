// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/moov-io/iso8583"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// emvTLVSpec declares DE 55 as a BER-TLV composite holding three EMV tags.
// Anything else inside DE 55 is unknown to the spec.
const emvTLVSpec = `
meta:
  name: "emv_bus"
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
  2:
    label: "PAN"
    type: "llvar_n"
    length: 19
    alias: "card.pan"
    enc: "ascii"
  55:
    label: "ICC Data"
    type: "lllvar"
    length: 999
    # Aliased on purpose: an alias is a second path into Data, and a value that
    # travels correctly under iso8583.field.55 can still be written as a Go
    # string under its alias and lose the whole message on the bus.
    alias: "icc_data"
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
`

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func newTLVCodec(t *testing.T, specPath string) *Gear {
	t.Helper()
	g := &Gear{}
	require.NoError(t, g.Init(&mockGearContext{
		config: map[string]any{"spec_path": specPath, "direction": "decode", "on_error": "reject"},
		logger: slog.Default(),
	}))
	return g
}

// buildISO packs a message with the same spec the gear uses, so the fixture
// cannot drift from the parser under test.
func buildISO(t *testing.T, specPath, mti, pan, iccHex string) []byte {
	t.Helper()
	spec, _, err := sdl.LoadSpec(specPath)
	require.NoError(t, err)

	icc, err := hex.DecodeString(iccHex)
	require.NoError(t, err)

	m := iso8583.NewMessage(spec)
	require.NoError(t, m.Field(0, mti))
	require.NoError(t, m.Field(2, pan))
	require.NoError(t, m.BinaryField(55, icc))
	packed, err := m.Pack()
	require.NoError(t, err)
	return packed
}

// TestTLVSurvivesTheBus is the Phase A acceptance test in its full form: a
// message carrying a TLV tag the spec does not model is decoded, serialized as
// CBOR exactly as it would be to cross a rack boundary, deserialized, and
// re-encoded. The bytes leaving must equal the bytes that arrived.
//
// Serialization is the step that matters. Preservation inside a single
// in-memory message proves nothing, because a switch re-encodes on a different
// gear, often on a different rack.
func TestTLVSurvivesTheBus(t *testing.T) {
	path := writeSpec(t, emvTLVSpec)
	g := newTLVCodec(t, path)

	// 9F02 (declared), 9F1F (NOT declared: private brand data), 5F2A (declared)
	icc := "9F0206000000000501" + "9F1F04DEADBEEF" + "5F2A020858"
	original := buildISO(t, path, "0100", "4111111111111111", icc)

	// --- decode ---
	in := &fluxmsg.FluxMsg{Data: map[string]any{}, Metadata: map[string]string{}, RawPayload: original}
	_, _, err := g.decode(in)
	require.NoError(t, err, "decode must accept a message with an unknown TLV tag")

	// --- cross the bus ---
	//
	// The whole message is marshalled and unmarshalled, exactly as the bus does
	// it. Marshalling alone proves nothing: the CBOR encoder writes an invalid
	// UTF-8 string without complaint and only the decoder rejects it, so a
	// defect here surfaces as a gear that silently never receives anything.
	full, err := cbor.Marshal(in)
	require.NoError(t, err, "a decoded message must be serializable")

	var arrived fluxmsg.FluxMsg
	require.NoError(t, cbor.Unmarshal(full, &arrived),
		"a decoded message must survive the bus; a Go string holding binary does not")

	crossed := arrived.Data

	// --- re-encode on the other side ---
	out := &fluxmsg.FluxMsg{
		Data:     crossed,
		Metadata: map[string]string{"iso8583.mti": "0100"},
	}
	_, _, err = g.encode(out)
	require.NoError(t, err)

	assert.Equal(t, hex.EncodeToString(original), hex.EncodeToString(out.RawPayload),
		"re-encoded message must be byte-identical, unknown tag included")

	// And the unknown tag is explicitly visible to downstream gears.
	assert.Contains(t, hex.EncodeToString(out.RawPayload), "9f1f04deadbeef",
		"the undeclared tag must still be on the wire")
}

// TestTLVArrivalOrderIsCanonicalized pins a limit of the fidelity above, so it
// is discovered here rather than against a scheme.
//
// TestTLVSurvivesTheBus builds its input with the same packer it asserts
// against, so the tags arrive already in the order the encoder emits. A real
// terminal emits them in whatever order it pleases. When that happens the tag
// values all survive, but the encoder re-emits them sorted by hex tag, so the
// re-encoded DE 55 is NOT byte-identical to the one that arrived.
//
// This is structural in the parser we adopted: subfields live in a map and the
// pack order comes from sorting its keys, so arrival order is never recorded.
// EMV assigns no meaning to the order of data objects inside DE 55, so parsing
// downstream is unaffected. Anything that treats the raw DE 55 blob as opaque
// bytes is affected: a MAC computed over the message as it arrived will not
// verify against the message as it leaves.
func TestTLVArrivalOrderIsCanonicalized(t *testing.T) {
	path := writeSpec(t, emvTLVSpec)
	spec, _, err := sdl.LoadSpec(path)
	require.NoError(t, err)

	// Arrival order as a terminal emits it: not sorted by tag.
	arrival, err := hex.DecodeString("9F0206000000000501" + "9F1F04DEADBEEF" + "5F2A020858")
	require.NoError(t, err)

	// Crafted by hand, because packing it would impose the sorted order this
	// test exists to observe. DE 55 only, ASCII LLL length prefix.
	wire := []byte("0100")
	wire = append(wire, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00)
	wire = append(wire, []byte(fmt.Sprintf("%03d", len(arrival)))...)
	wire = append(wire, arrival...)

	m := iso8583.NewMessage(spec)
	require.NoError(t, m.Unpack(wire))

	out, err := m.Pack()
	require.NoError(t, err)

	assert.NotEqual(t, hex.EncodeToString(wire), hex.EncodeToString(out),
		"if this now matches, arrival order is being preserved and the comment above is stale")

	// Nothing is lost, only reordered: every tag and value still on the wire.
	outHex := hex.EncodeToString(out)
	for _, tlv := range []string{"9f0206000000000501", "9f1f04deadbeef", "5f2a020858"} {
		assert.Contains(t, outHex, tlv, "tag/value must survive even when reordered")
	}
	assert.Equal(t, len(wire), len(out), "reordering must not change the byte count")
}

// TestBinaryFieldSurvivesTheBus covers the general case behind the same defect:
// any binary field, not only composites, has to cross as bytes.
func TestBinaryFieldSurvivesTheBus(t *testing.T) {
	path := writeSpec(t, emvTLVSpec)
	g := newTLVCodec(t, path)

	icc := "9F0206000000000501"
	original := buildISO(t, path, "0100", "4111111111111111", icc)

	in := &fluxmsg.FluxMsg{Data: map[string]any{}, Metadata: map[string]string{}, RawPayload: original}
	_, _, err := g.decode(in)
	require.NoError(t, err)

	raw, ok := in.Get("iso8583.field.55")
	require.True(t, ok, "DE 55 must be present in Data")
	_, isBytes := raw.([]byte)
	assert.True(t, isBytes, "a binary field must be stored as bytes, not as a string")

	_, err = cbor.Marshal(in.Data)
	require.NoError(t, err)
}
