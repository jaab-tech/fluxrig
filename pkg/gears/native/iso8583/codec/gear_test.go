// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/moov-io/iso8583"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGearContext struct {
	sdk.GearContext
	config map[string]any
	logger *slog.Logger
}

func (m *mockGearContext) Config() map[string]any { return m.config }
func (m *mockGearContext) Logger() *slog.Logger   { return m.logger }

func initGear(t *testing.T, specFile string, extras ...map[string]any) *Gear {
	t.Helper()
	specPath := filepath.Join("sdl", "testdata", specFile)
	config := map[string]any{
		"spec_path": specPath,
		"direction": "auto",
	}
	if len(extras) > 0 {
		for k, v := range extras[0] {
			config[k] = v
		}
	}
	g := &Gear{}
	ctx := &mockGearContext{config: config, logger: slog.Default()}
	err := g.Init(ctx)
	require.NoError(t, err)
	return g
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec: generic_ascii (7 fields: MTI, PAN, ProcCode, Amount, STAN, RespCode, TermID)
// ─────────────────────────────────────────────────────────────────────────────

func TestASCII_Decode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0800")
	_ = moovMsg.Field(11, "123456")
	packed, _ := moovMsg.Pack()

	msg := fluxmsg.New()
	msg.RawPayload = packed

	processed, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	stan, ok := processed.Get("stan")
	assert.True(t, ok)
	assert.Equal(t, "123456", stan)
	assert.Equal(t, "0800", processed.Metadata["iso8583.mti"])
	assert.NotEmpty(t, processed.Metadata["codec.spec_hash"])
}

func TestASCII_Encode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0210"
	_ = msg.Set("card.pan", "4111111111111111")
	_ = msg.Set("stan", "654321")

	processed, err := g.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.NotEmpty(t, processed.RawPayload)

	isoMsg := iso8583.NewMessage(g.moovSpec)
	require.NoError(t, isoMsg.Unpack(processed.RawPayload))
	pan, _ := isoMsg.GetString(2)
	assert.Equal(t, "4111111111111111", pan)
	stan, _ := isoMsg.GetString(11)
	assert.Equal(t, "654321", stan)
}

func TestASCII_RoundTrip(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0200"
	_ = msg.Set("card.pan", "4111111111111111")
	_ = msg.Set("stan", "111222")
	_ = msg.Set("proc_code", "000000")

	// Encode
	encMsg, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	// Decode
	decMsg := fluxmsg.New()
	decMsg.RawPayload = encMsg.RawPayload
	processed, err := g.Process(context.Background(), decMsg)
	require.NoError(t, err)

	val, _ := processed.Get("card.pan")
	assert.Equal(t, "4111111111111111", val)
	val, _ = processed.Get("stan")
	assert.Equal(t, "111222", val)
}

func TestASCII_AllFields(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0200"
	_ = msg.Set("card.pan", "5500000000000004")
	_ = msg.Set("proc_code", "003000")
	_ = msg.Set("amount", "000000001500")
	_ = msg.Set("stan", "999888")
	_ = msg.Set("response_code", "00")
	_ = msg.Set("terminal_id", "TERM0001")

	// Encode
	enc, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	// Decode
	dec := fluxmsg.New()
	dec.RawPayload = enc.RawPayload
	res, err := g.Process(context.Background(), dec)
	require.NoError(t, err)

	v, _ := res.Get("card.pan")
	assert.Equal(t, "5500000000000004", v)
	v, _ = res.Get("amount")
	// moov-io Numeric may strip leading zeros; verify semantic value
	assert.Contains(t, v, "1500")
	v, _ = res.Get("terminal_id")
	assert.Equal(t, "TERM0001", v)
	assert.Equal(t, "0200", res.Metadata["iso8583.mti"])
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec: minimal (2 fields: MTI + ResponseCode)
// ─────────────────────────────────────────────────────────────────────────────

func TestMinimal_RoundTrip(t *testing.T) {
	g := initGear(t, "minimal.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0810"
	_ = msg.Set("response_code", "00")

	enc, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	dec := fluxmsg.New()
	dec.RawPayload = enc.RawPayload
	res, err := g.Process(context.Background(), dec)
	require.NoError(t, err)

	v, _ := res.Get("response_code")
	assert.Equal(t, "00", v)
	assert.Equal(t, "0810", res.Metadata["iso8583.mti"])
}

func TestMinimal_DecodeNoFields(t *testing.T) {
	g := initGear(t, "minimal.yaml")

	// Pack message with only MTI, no field 39
	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0800")
	packed, _ := moovMsg.Pack()

	msg := fluxmsg.New()
	msg.RawPayload = packed
	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	// response_code should NOT be present
	_, ok := res.Get("response_code")
	assert.False(t, ok)
	assert.Equal(t, "0800", res.Metadata["iso8583.mti"])
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec: bcd_basic (15 fields with BCD encoding)
// ─────────────────────────────────────────────────────────────────────────────

func TestBCD_RoundTrip(t *testing.T) {
	g := initGear(t, "bcd_basic.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0200"
	_ = msg.Set("card.pan", "4000123456789012")
	_ = msg.Set("proc_code", "000000")
	_ = msg.Set("amount", "000000050000")
	_ = msg.Set("stan", "000001")
	_ = msg.Set("rrn", "240213000001")
	_ = msg.Set("terminal_id", "T0000001")
	_ = msg.Set("merchant_id", "M00000000000001")
	_ = msg.Set("currency", "840")

	// Encode
	enc, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	// Decode
	dec := fluxmsg.New()
	dec.RawPayload = enc.RawPayload
	res, err := g.Process(context.Background(), dec)
	require.NoError(t, err)

	v, _ := res.Get("card.pan")
	assert.Equal(t, "4000123456789012", v)
	v, _ = res.Get("rrn")
	assert.Equal(t, "240213000001", v)
	v, _ = res.Get("currency")
	assert.Equal(t, "840", v)
}

func TestBCD_PartialFields(t *testing.T) {
	g := initGear(t, "bcd_basic.yaml")

	// Only STAN and response_code
	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0810"
	_ = msg.Set("stan", "999999")
	_ = msg.Set("response_code", "00")

	enc, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	dec := fluxmsg.New()
	dec.RawPayload = enc.RawPayload
	res, err := g.Process(context.Background(), dec)
	require.NoError(t, err)

	v, _ := res.Get("stan")
	assert.Equal(t, "999999", v)
	v, _ = res.Get("response_code")
	assert.Equal(t, "00", v)
	// Others should be absent
	_, ok := res.Get("card.pan")
	assert.False(t, ok)
}

// ─────────────────────────────────────────────────────────────────────────────
// Error Paths
// ─────────────────────────────────────────────────────────────────────────────

func TestError_GarbageBytes(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	msg := fluxmsg.New()
	msg.RawPayload = []byte{0xFF, 0xFE, 0x01, 0x02, 0x03}

	// Default on_error=drop → should return nil, nil
	res, err := g.Process(context.Background(), msg)
	assert.Nil(t, res)
	assert.Nil(t, err)
}

func TestError_GarbageBytes_Reject(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml", map[string]any{"on_error": "reject"})

	msg := fluxmsg.New()
	msg.RawPayload = []byte{0xFF, 0xFE, 0x01, 0x02, 0x03}

	// on_error=reject → return msg with error
	res, err := g.Process(context.Background(), msg)
	assert.NotNil(t, res)
	assert.Error(t, err)
}

func TestError_TruncatedMessage(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	// Pack a valid message, then truncate it
	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0200")
	_ = moovMsg.Field(11, "123456")
	packed, _ := moovMsg.Pack()

	// Truncate at half
	msg := fluxmsg.New()
	msg.RawPayload = packed[:len(packed)/2]

	res, err := g.Process(context.Background(), msg)
	assert.Nil(t, res) // on_error=drop
	assert.Nil(t, err)
}

func TestError_EmptyPayloadDecodeMode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml", map[string]any{"direction": "decode"})

	msg := fluxmsg.New()
	msg.RawPayload = []byte{} // Empty but forced decode

	res, err := g.Process(context.Background(), msg)
	assert.Nil(t, res) // on_error=drop
	assert.Nil(t, err)
}

func TestError_MissingSpec(t *testing.T) {
	g := &Gear{}
	ctx := &mockGearContext{
		config: map[string]any{
			"spec_path": "nonexistent/path.yaml",
		},
		logger: slog.Default(),
	}
	err := g.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load SDL spec")
}

func TestError_MissingSpecPath(t *testing.T) {
	g := &Gear{}
	ctx := &mockGearContext{
		config: map[string]any{},
		logger: slog.Default(),
	}
	err := g.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing 'spec_path'")
}

// ─────────────────────────────────────────────────────────────────────────────
// Direction Detection
// ─────────────────────────────────────────────────────────────────────────────

func TestDirection_AutoDecode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0200")
	packed, _ := moovMsg.Pack()

	msg := fluxmsg.New()
	msg.RawPayload = packed

	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, "0200", res.Metadata["iso8583.mti"])
}

func TestDirection_AutoEncode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0200"
	_ = msg.Set("stan", "111111")

	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.NotEmpty(t, res.RawPayload)
}

func TestDirection_ForcedDecode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml", map[string]any{"direction": "decode"})

	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0200")
	_ = moovMsg.Field(11, "555555")
	packed, _ := moovMsg.Pack()

	msg := fluxmsg.New()
	msg.RawPayload = packed

	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	v, _ := res.Get("stan")
	assert.Equal(t, "555555", v)
}

func TestDirection_ForcedEncode(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml", map[string]any{"direction": "encode"})

	msg := fluxmsg.New()
	msg.Metadata["iso8583.mti"] = "0200"
	_ = msg.Set("stan", "444444")

	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.NotEmpty(t, res.RawPayload)
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata Persistence
// ─────────────────────────────────────────────────────────────────────────────

func TestMetadata_SpecHash(t *testing.T) {
	g := initGear(t, "generic_ascii.yaml")

	moovMsg := iso8583.NewMessage(g.moovSpec)
	_ = moovMsg.Field(0, "0200")
	packed, _ := moovMsg.Pack()

	msg := fluxmsg.New()
	msg.RawPayload = packed

	res, err := g.Process(context.Background(), msg)
	require.NoError(t, err)

	hash := res.Metadata["codec.spec_hash"]
	assert.Len(t, hash, 12)
	assert.Equal(t, g.meta.SpecHash, hash)
}

func TestMetadata_DifferentSpecsDifferentHashes(t *testing.T) {
	g1 := initGear(t, "generic_ascii.yaml")
	g2 := initGear(t, "minimal.yaml")
	g3 := initGear(t, "bcd_basic.yaml")

	assert.NotEqual(t, g1.meta.SpecHash, g2.meta.SpecHash)
	assert.NotEqual(t, g1.meta.SpecHash, g3.meta.SpecHash)
	assert.NotEqual(t, g2.meta.SpecHash, g3.meta.SpecHash)
}

// ─────────────────────────────────────────────────────────────────────────────
// Value Masking
// ─────────────────────────────────────────────────────────────────────────────

func TestMaskValue(t *testing.T) {
	assert.Equal(t, "****", maskValue("1234"))
	assert.Equal(t, "**********", maskValue("1234567890"))
	assert.Equal(t, "411111******1111", maskValue("4111111111111111"))
}
