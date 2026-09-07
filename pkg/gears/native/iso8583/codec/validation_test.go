// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/moov-io/iso8583"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// packed builds a message from field values and returns its bytes.
func packed(t *testing.T, g *Gear, mti string, kv map[int]string) []byte {
	t.Helper()
	m := iso8583.NewMessage(g.moovSpec)
	require.NoError(t, m.Field(0, mti))
	for de, v := range kv {
		require.NoError(t, m.Field(de, v))
	}
	raw, err := m.Pack()
	require.NoError(t, err)
	return raw
}

func processed(t *testing.T, g *Gear, raw []byte) (*fluxmsg.FluxMsg, error) {
	t.Helper()
	msg := fluxmsg.New()
	msg.RawPayload = raw
	return g.Process(context.Background(), msg)
}

// A well-formed request, and one missing a mandatory element.
func requests(t *testing.T, g *Gear) (good, bad []byte) {
	t.Helper()
	good = packed(t, g, "0200", map[int]string{
		2: "4111111111111111", 4: "000000010000", 11: "000001",
	})
	bad = packed(t, g, "0200", map[int]string{
		4: "000000010000", 11: "000001", // no DE 2, which is mandatory
	})
	return good, bad
}

// The default has to be off. A message accepted yesterday must not be rejected
// today because the code was upgraded, and warning on every message is a cost an
// operator did not ask for.
func TestValidationIsOffUnlessAskedFor(t *testing.T) {
	g := initGear(t, "validating.yaml")
	require.Equal(t, ValidationOff, g.validation)
	require.Nil(t, g.validator, "the validator was compiled for a gear that will never use it")

	_, bad := requests(t, g)
	res, err := processed(t, g, bad)
	require.NoError(t, err, "a rule fired with validation off")
	require.NotNil(t, res)
	require.NotContains(t, res.Metadata, "codec.violations")
}

// Warning is how an operator finds out whether their spec matches their traffic,
// before deciding to enforce it. Nothing is rejected.
func TestWarningRecordsAndLetsThrough(t *testing.T) {
	g := initGear(t, "validating.yaml", map[string]any{"validation": ValidationWarn})
	require.NotNil(t, g.validator)

	_, bad := requests(t, g)
	res, err := processed(t, g, bad)
	require.NoError(t, err, "warning rejected a message")
	require.NotNil(t, res, "warning dropped a message")
	require.Equal(t, "1", res.Metadata["codec.violations"],
		"what was found does not travel with the message")
}

// Enforcing is the decision made afterwards, and then the rule fails the
// message.
func TestEnforcingRejectsAMessageThatBreaksARule(t *testing.T) {
	g := initGear(t, "validating.yaml", map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "reject",
	})

	good, bad := requests(t, g)

	_, err := processed(t, g, good)
	require.NoError(t, err, "a valid message was rejected")

	_, err = processed(t, g, bad)
	require.Error(t, err, "a message missing a mandatory element was accepted")
	// The error says which spec and which rule: an operator reading it should
	// not have to go and find out what was wrong.
	require.Contains(t, err.Error(), "validating")
	require.Contains(t, err.Error(), "1.0.0")
	require.Contains(t, err.Error(), "DE 2")
	require.Contains(t, err.Error(), "mandatory")
}

// A `warn` check does not reject even while enforcing. That is the whole point
// of severity living on the rule rather than on the deployment.
func TestAWarningCheckDoesNotRejectWhileEnforcing(t *testing.T) {
	g := initGear(t, "validating.yaml", map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "reject",
	})

	zero := packed(t, g, "0200", map[int]string{
		2: "4111111111111111", 4: "000000000000", 11: "000001",
	})
	res, err := processed(t, g, zero)
	require.NoError(t, err, "a check marked warn rejected the message")
	require.Equal(t, "1", res.Metadata["codec.violations"], "the check did not fire at all")
}

// A value outside a closed set is a rejection, and it is found on the response
// where the field is mandatory.
func TestAValueOutsideAClosedSetIsRejectedOnTheWire(t *testing.T) {
	g := initGear(t, "validating.yaml", map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "reject",
	})

	ok := packed(t, g, "0210", map[int]string{4: "000000010000", 11: "000001", 39: "00"})
	_, err := processed(t, g, ok)
	require.NoError(t, err)

	nope := packed(t, g, "0210", map[int]string{4: "000000010000", 11: "000001", 39: "99"})
	_, err = processed(t, g, nope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DE 39")
	require.Contains(t, err.Error(), "closed")
}

// on_error decides what a rejection does, exactly as it does for a decode
// failure. An operator who chose to drop bad messages does not get a different
// answer because this one was well formed and wrong rather than malformed.
func TestARejectionHonoursOnError(t *testing.T) {
	g := initGear(t, "validating.yaml", map[string]any{
		"validation": ValidationEnforce,
		"on_error":   "drop",
	})
	_, bad := requests(t, g)
	res, err := processed(t, g, bad)
	require.NoError(t, err)
	require.Nil(t, res, "on_error: drop did not drop a rejected message")
}

// A spec whose rules cannot compile must fail at boot. A Rack that started has
// already told the Mixer it is serving this protocol.
func TestAnUnknownValidationModeFailsAtBoot(t *testing.T) {
	specPath := "sdl/testdata/validating.yaml"
	g := &Gear{}
	err := g.Init(&mockGearContext{
		config: map[string]any{"spec_path": specPath, "validation": "sometimes"},
		logger: slog.Default(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sometimes")
	require.Contains(t, strings.ToLower(err.Error()), "validation")
}

// The manifest is the gear's published contract, and it drifted: it declared
// on_error defaulting to "reject" while the code defaulted to "drop", so an
// operator reading the manifest configured for one behaviour and got the other.
// Nothing had ever compared the two.
func TestTheManifestDeclaresWhatTheCodeActuallyDoes(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum    []string `json:"enum"`
			Default string   `json:"default"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte((&Gear{}).Manifest().ConfigSchema), &schema))

	// Every default the manifest states is what a gear given no such key gets.
	g := initGear(t, "validating.yaml")
	actual := map[string]string{
		"direction":  g.direction,
		"on_error":   g.onError,
		"validation": g.validation,
	}
	for key, want := range actual {
		prop, ok := schema.Properties[key]
		require.True(t, ok, "the manifest does not document %q", key)
		require.Equal(t, want, prop.Default,
			"the manifest says %q defaults to %q; the code uses %q", key, prop.Default, want)
		require.Contains(t, prop.Enum, want, "the default of %q is not among its own values", key)
	}

	// And every value the manifest offers is one the gear accepts.
	for _, mode := range schema.Properties["validation"].Enum {
		gm := &Gear{}
		err := gm.Init(&mockGearContext{
			config: map[string]any{"spec_path": "sdl/testdata/validating.yaml", "validation": mode},
			logger: slog.Default(),
		})
		require.NoError(t, err, "the manifest offers validation %q and the gear refuses it", mode)
	}
}
