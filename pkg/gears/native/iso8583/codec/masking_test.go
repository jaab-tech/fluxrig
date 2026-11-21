// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// closedPAN is a PAN-shaped value outside the closed set of pii_closed.yaml.
// It stands for any PII a closed set refuses: the assertions below fail if it
// reaches any sink.
const closedPAN = "4111111111110000"

// piiGear builds a gear over the closed-PAN spec with a logger that writes to
// logs, so the test reads what the warn log would carry.
func piiGear(t *testing.T, logs *bytes.Buffer, extras map[string]any) *Gear {
	t.Helper()
	config := map[string]any{
		"spec_path": "sdl/testdata/pii_closed.yaml",
		"direction": "auto",
	}
	for k, v := range extras {
		config[k] = v
	}
	g := &Gear{}
	ctx := &mockGearContext{config: config, logger: slog.New(slog.NewTextHandler(logs, nil))}
	require.NoError(t, g.Init(ctx))
	return g
}

// piiRequest packs a 0200 carrying a PAN the closed set does not list.
func piiRequest(t *testing.T, g *Gear) []byte {
	t.Helper()
	return packed(t, g, "0200", map[int]string{
		2: closedPAN, 4: "000000010000", 11: "000001",
	})
}

func processRaw(t *testing.T, g *Gear, raw []byte) (*fluxmsg.FluxMsg, error) {
	t.Helper()
	msg := fluxmsg.New()
	msg.RawPayload = raw
	return g.Process(context.Background(), msg)
}

// A value outside a closed PII set must not reach the warn log. The field
// path and the rule stay: an operator reading it learns what broke, not the
// value that broke it.
func TestClosedSetValueIsWithheldFromTheWarnLog(t *testing.T) {
	var logs bytes.Buffer
	g := piiGear(t, &logs, map[string]any{"validation": ValidationWarn})

	res, err := processRaw(t, g, piiRequest(t, g))
	require.NoError(t, err, "warning records and lets through")
	require.NotNil(t, res)

	out := logs.String()
	require.Contains(t, out, "DE 2", "the field path stays in the log")
	require.Contains(t, out, "closed", "the rule stays in the log")
	require.NotContains(t, out, closedPAN, "the PAN reached the warn log")
}

// The same value must not reach the rejection error, which travels in
// error.message metadata and in API responses.
func TestClosedSetValueIsWithheldFromTheRejectionError(t *testing.T) {
	var logs bytes.Buffer
	g := piiGear(t, &logs, map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "kill", // the error comes back, so its text can be read
	})

	_, err := processRaw(t, g, piiRequest(t, g))
	require.Error(t, err, "a value outside the closed set is rejected")
	require.Contains(t, err.Error(), "DE 2", "the field path stays in the error")
	require.NotContains(t, err.Error(), closedPAN, "the PAN reached the rejection error")
}

// Nor the error port metadata of a rejected message.
func TestClosedSetValueIsWithheldFromTheErrorMetadata(t *testing.T) {
	var logs bytes.Buffer
	g := piiGear(t, &logs, map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "reject",
	})
	ctx := g.emitter.(*mockGearContext)

	res, err := processRaw(t, g, piiRequest(t, g))
	require.NoError(t, err)
	require.Nil(t, res, "a rejected message leaves on the error port")
	rejected := ctx.emitted["error"]
	require.Len(t, rejected, 1)
	require.Equal(t, ReasonViolation, rejected[0].Metadata["error.reason"])
	require.Contains(t, rejected[0].Metadata["error.message"], "DE 2")
	require.NotContains(t, rejected[0].Metadata["error.message"], closedPAN,
		"the PAN reached error.message metadata")
	require.NotContains(t, rejected[0].Metadata["codec.violations"], closedPAN,
		"the PAN reached codec metadata")
}
