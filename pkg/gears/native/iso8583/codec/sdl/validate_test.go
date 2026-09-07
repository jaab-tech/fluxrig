// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func refValidator(t *testing.T) *Validator {
	t.Helper()
	raw, err := os.ReadFile(refSpec)
	require.NoError(t, err)
	spec, err := ParseSemantic(raw)
	require.NoError(t, err)
	v, err := NewValidator(spec)
	require.NoError(t, err)
	return v
}

// reasons renders violations for a failure message.
func reasons(vs []Violation) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Severity+" "+v.String())
	}
	return strings.Join(out, "\n  ")
}

// A well-formed authorization request passes. If a validator rejects the traffic
// the spec was written from, it is the validator that is wrong.
func TestAGoodMessagePasses(t *testing.T) {
	v := refValidator(t)
	m := msg("0100",
		"2", "4111111111111111",
		"3", "000000",
		"4", "000000010000",
		"7", "0906120000",
		"11", "000001",
		"22", "90", // magnetic stripe: DE 55 forbidden, DE 14 not required
		"35", "4111111111111111=2812",
		"41", "TERM0001",
		"42", "MERCHANT000001",
		"49", "858",
	)
	got := v.Validate(m)
	require.Empty(t, got, "a valid request was rejected:\n  %s", reasons(got))
}

// The rule that was documentation until now: a mandatory element that is not
// there fails the message.
func TestAMissingMandatoryElementIsRejected(t *testing.T) {
	v := refValidator(t)
	m := msg("0100", "3", "000000", "4", "000000010000", "22", "90", "35", "x=2812")

	got := v.Validate(m)
	require.True(t, Rejects(got))

	var sawPAN bool
	for _, x := range got {
		if x.DE == 2 && x.Kind == ViolationUsage {
			sawPAN = true
			require.Equal(t, SeverityReject, x.Severity)
			require.Contains(t, x.Reason, "mandatory")
		}
	}
	require.True(t, sawPAN, "DE 2 is mandatory on an 0100 and its absence went unreported:\n  %s", reasons(got))
}

// The condition decides whether the rule applies at all. DE 55 is mandatory when
// the terminal read a chip and forbidden when it read a stripe, and the same
// message is right or wrong depending only on DE 22.
func TestAConditionalRuleAppliesOnlyWhereItsConditionHolds(t *testing.T) {
	v := refValidator(t)
	base := []string{
		"2", "4111111111111111", "3", "000000", "4", "000000010000",
		"7", "0906120000", "11", "000001", "41", "TERM0001",
		"42", "MERCHANT000001", "49", "858", "35", "4111111111111111=2812",
	}

	chipNoICC := msg("0100", append(append([]string{}, base...), "22", "05")...)
	got := v.Validate(chipNoICC)
	require.True(t, Rejects(got), "a chip read with no DE 55 passed:\n  %s", reasons(got))
	require.Contains(t, reasons(got), "DE 55")
	// The reason says what made the rule apply, or a rejection is unreadable.
	require.Contains(t, reasons(got), "field(22)")

	stripeWithICC := msg("0100", append(append([]string{}, base...), "22", "90", "55", "9F02...")...)
	got = v.Validate(stripeWithICC)
	require.True(t, Rejects(got), "a stripe read carrying DE 55 passed:\n  %s", reasons(got))
	require.Contains(t, reasons(got), "must not be present")

	chipWithICC := msg("0100", append(append([]string{}, base...), "22", "05", "55", "9F02...")...)
	require.Empty(t, v.Validate(chipWithICC), "a chip read carrying DE 55 was rejected")
}

// A closed value set is a claim that those are all the values there are. An open
// one lists what is known and permits the rest, so it is not a rule.
func TestAValueOutsideAClosedSetIsRejected(t *testing.T) {
	spec, err := ParseSemantic([]byte(`
spec:
  id: acme
  name: Acme
  version: "1.0.0"
  protocol: iso8583
  wire:
    format: moov
    source: "moov:spec87ascii"
  messages:
    catalog:
      "0200": {name: Request, flow: request, pairs_with: "0210"}
      "0210": {name: Response, flow: response, pairs_with: "0200"}
  enums:
    tight:
      closed: true
      values:
        "00": {name: Approved}
        "05": {name: Declined}
    loose:
      values:
        "01": {name: Known}
  fields:
    39:
      name: Response Code
      values_ref: tight
      messages:
        - {mti: "0210", usage: mandatory}
    44:
      name: Additional Response Data
      values_ref: loose
      messages:
        - {mti: "0210", usage: optional}
`))
	require.NoError(t, err)
	v, err := NewValidator(spec)
	require.NoError(t, err)

	require.Empty(t, v.Validate(msg("0210", "39", "00")))

	got := v.Validate(msg("0210", "39", "99"))
	require.True(t, Rejects(got), "a value outside a closed set passed")
	require.Equal(t, ViolationValue, got[0].Kind)
	require.Contains(t, got[0].Reason, "closed")
	require.Contains(t, got[0].Reason, "00, 05")

	// The open set permits what it never listed.
	require.Empty(t, v.Validate(msg("0210", "39", "00", "44", "anything at all")))
}

// A check is the scheme manual's edit criteria made executable, and its severity
// is what makes one deployable to a live fleet before it is enforced.
func TestChecksCarryTheirOwnSeverity(t *testing.T) {
	v := refValidator(t)
	// No PAN and no track 2: "Credential present" fails, and it rejects.
	m := msg("0100", "3", "000000", "4", "000000010000", "7", "0906120000",
		"11", "000001", "22", "90", "41", "TERM0001", "42", "MERCHANT000001", "49", "858")

	var credential, amount *Violation
	for i, x := range v.Validate(m) {
		if x.Kind != ViolationCheck {
			continue
		}
		got := v.Validate(m)[i]
		switch x.Rule {
		case "Credential present":
			credential = &got
		case "Amount is positive":
			amount = &got
		}
	}
	require.NotNil(t, credential, "the credential check did not fire on a message with neither PAN nor track 2")
	require.Equal(t, SeverityReject, credential.Severity)

	// A zero amount fails a check the spec marked warn: recorded, not rejected.
	zero := msg("0100", "2", "4111111111111111", "3", "000000", "4", "0",
		"7", "0906120000", "11", "000001", "22", "90", "35", "x=2812",
		"41", "TERM0001", "42", "MERCHANT000001", "49", "858")
	var warned bool
	for _, x := range v.Validate(zero) {
		if x.Kind == ViolationCheck && x.Rule == "Amount is positive" {
			warned = true
			require.Equal(t, SeverityWarn, x.Severity, "a warning check rejected a message")
		}
	}
	require.True(t, warned, "the amount check did not fire on a zero amount")
	require.False(t, Rejects(v.Validate(zero)), "a warning rejected the message")
	_ = amount
}

// Everything wrong with a message, not the first thing checked: whoever reads a
// rejection wants to know what to fix.
func TestEveryViolationIsReported(t *testing.T) {
	v := refValidator(t)
	got := v.Validate(msg("0100", "22", "05"))
	require.Greater(t, len(got), 3, "only %d violations on a nearly empty request:\n  %s", len(got), reasons(got))
}

// A message type the spec says nothing about is not a message this spec can
// judge. Rejecting it would make an unrelated protocol's traffic fail here.
func TestAnUnknownMessageTypeHasNoRulesToBreak(t *testing.T) {
	v := refValidator(t)
	require.Empty(t, v.Validate(msg("9999", "2", "4111111111111111")))
}

// A severity nobody recognises would fall through to the strict default, so a
// typo would start rejecting traffic. It is refused at load instead.
func TestAnUnknownSeverityIsRefusedAtLoad(t *testing.T) {
	// The rule is enforced by the resolver, which runs at load. ParseSemantic
	// only reads the document -- it is what the docs generator uses, and it is
	// deliberately not the thing that decides whether a spec may run.
	_, _, err := LoadSpecContent([]byte(`
spec:
  id: acme
  name: Acme
  version: "1.0.0"
  wire: {format: moov, source: "moov:spec87ascii"}
  messages:
    catalog:
      "0200": {name: Request, flow: request, pairs_with: "0210"}
  fields:
    2: {name: PAN}
  checks:
    - mti: ["0200"]
      name: "Typo"
      assert: "present(2)"
      severity: rejct
`), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "severity")
	require.Contains(t, err.Error(), "rejct")
}

// How an element compares is the spec's to say, and it says it with
// `format.kind` -- a vocabulary the reference spec has used since it was
// migrated and comparison ignored. Without it a rule on an amount would have to
// write the element's own zero padding into itself.
func TestAnElementComparesTheWayItsKindSaysItDoes(t *testing.T) {
	spec, err := ParseSemantic([]byte(`
spec:
  id: kinds
  name: Kinds
  version: "1.0.0"
  protocol: iso8583
  wire: {format: moov, source: "moov:spec87ascii"}
  messages:
    catalog:
      "0200": {name: Request, flow: request, pairs_with: "0210"}
      "0210": {name: Response, flow: response, pairs_with: "0200"}
  fields:
    4:
      name: Amount
      format: {kind: amount, currency_field: 49}
      messages:
        - {mti: "0200", usage: mandatory}
    11:
      name: STAN
      format: {kind: numeric}
      messages:
        - {mti: "0200", usage: mandatory}
    39:
      name: Response Code
      messages:
        - {mti: "0200", usage: forbidden, when: "field(4) == 1000"}
    49:
      name: Currency
      messages:
        - {mti: "0200", usage: optional}
  checks:
    - {mti: ["0200"], name: "Amount is ten", assert: "field(4) == 1000"}
    - {mti: ["0200"], name: "Stan is one", assert: "field(11) == 1"}
    - {mti: ["0200"], name: "Currency is not oh-two", assert: "field(49) != '02'"}
`))
	require.NoError(t, err)
	v, err := NewValidator(spec)
	require.NoError(t, err)

	// An amount written with the element's own padding equals the number the
	// rule was written with.
	padded := msg("0200", "4", "000000001000", "11", "000001", "49", "0858")
	got := v.Validate(padded)
	require.Empty(t, got, "a padded amount did not equal the number a rule compares it to:\n  %s", reasons(got))

	// A field with no kind still compares as characters: "0858" is not "858",
	// which is the rule that keeps a response code of "00" from being "0".
	require.NotEqual(t, "858", "0858")
	notPadded := msg("0200", "4", "1000", "11", "1", "49", "02")
	got = v.Validate(notPadded)
	var currency bool
	for _, x := range got {
		if x.Rule == "Currency is not oh-two" {
			currency = true
		}
	}
	require.True(t, currency, "an element with no kind compared as a number")

	// And the condition on DE 39 read the amount as a number too, so the rule
	// fired on the padded message.
	withCode := msg("0200", "4", "000000001000", "11", "000001", "39", "00")
	got = v.Validate(withCode)
	require.True(t, Rejects(got), "a condition on an amount did not fire on the padded value")
	require.Contains(t, reasons(got), "DE 39")
}

// A PAN is an identifier that happens to be digits. A leading zero on one is a
// different card, so it is deliberately not compared as a number.
func TestAPANIsNotANumber(t *testing.T) {
	require.False(t, comparesNumerically("pan"))
	require.False(t, comparesNumerically(""))
	for _, k := range []string{"amount", "date", "time", "datetime", "numeric"} {
		require.True(t, comparesNumerically(k), "%s should compare as a number", k)
	}
}

// Until a spec says otherwise, everything compares as characters. Parsing
// happens where there is no spec -- the resolver checks every expression in a
// document before the document is known to be coherent -- so the unbound reading
// has to be the safe one.
func TestAnUnboundExpressionComparesAsText(t *testing.T) {
	e, err := ParseWhen("field(4) == 1000")
	require.NoError(t, err)
	require.False(t, e.Eval(msg("0200", "4", "000000001000")), "an unbound expression compared as a number")
	require.True(t, e.Eval(msg("0200", "4", "1000")))

	e.BindKinds(func(FieldRef) string { return "amount" })
	require.True(t, e.Eval(msg("0200", "4", "000000001000")), "binding did not change how it compares")
}
