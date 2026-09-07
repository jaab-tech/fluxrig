// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeMsg is a message spelled out by hand. Paths are written the way an
// expression writes them: "39", "55.9F02".
type fakeMsg struct {
	mti    string
	fields map[string]string
}

func (m fakeMsg) MTI() string { return m.mti }

func (m fakeMsg) Field(de int, subs []string) (string, bool) {
	key := FieldRef{DE: de, Subs: subs}.String()
	v, ok := m.fields[key]
	return v, ok
}

func msg(mti string, kv ...string) fakeMsg {
	f := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		f[kv[i]] = kv[i+1]
	}
	return fakeMsg{mti: mti, fields: f}
}

func readRefSpec(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(refSpec)
	require.NoError(t, err)
	return raw
}

func evalWhen(t *testing.T, src string, m Subject) bool {
	t.Helper()
	e, err := ParseWhen(src)
	require.NoError(t, err, "expression did not parse: %s", src)
	return e.Eval(m)
}

// The expressions the reference spec actually writes, answered against messages
// that do and do not satisfy them. Until now these were parsed and thrown away.
func TestTheExpressionsTheReferenceSpecWritesAreAnswered(t *testing.T) {
	contactChip := msg("0100", "22", "05", "55", "9F02...")
	magStripe := msg("0100", "22", "90", "35", "track2")

	cases := []struct {
		when string
		m    Subject
		want bool
	}{
		// DE 55 is mandatory when the terminal read a chip.
		{"field(22) == '05' || field(22) == '07'", contactChip, true},
		{"field(22) == '05' || field(22) == '07'", magStripe, false},
		// ...and forbidden when it read a stripe.
		{"field(22) == '02' || field(22) == '90'", magStripe, true},
		{"field(22) == '02' || field(22) == '90'", contactChip, false},
		// DE 14 is mandatory when there is no track 2 to carry the expiry.
		{"present(35) == false", contactChip, true},
		{"present(35) == false", magStripe, false},
		// present() stands alone as a condition, which the grammar allows.
		{"present(35)", magStripe, true},
		{"present(35)", contactChip, false},
	}
	for _, c := range cases {
		if got := evalWhen(t, c.when, c.m); got != c.want {
			t.Errorf("%s against %s: got %v, want %v", c.when, c.m.MTI(), got, c.want)
		}
	}
}

func TestTheOperatorsAndTheirCombinations(t *testing.T) {
	m := msg("0210", "39", "00", "4", "000000012345", "3", "000000")

	cases := []struct {
		when string
		want bool
	}{
		{"mti == '0210'", true},
		{"mti != '0210'", false},
		{"field(39) == '00'", true},
		{"field(39) != '00'", false},
		{"field(4) > 1000", true},
		{"field(4) < 1000", false},
		{"field(4) >= 12345", true},
		{"field(4) <= 12344", false},
		{"field(39) == '00' && mti == '0210'", true},
		{"field(39) == '00' && mti == '0110'", false},
		{"field(39) == '99' || mti == '0210'", true},
		{"!(field(39) == '99')", true},
		{"(field(39) == '00' || field(39) == '10') && present(4)", true},
		{"true", true},
		{"false", false},
	}
	for _, c := range cases {
		if got := evalWhen(t, c.when, m); got != c.want {
			t.Errorf("%s: got %v, want %v", c.when, got, c.want)
		}
	}
}

// Equality is on the characters, not on the number they spell. A response code
// of "00" is not "0", and reading them as numbers would make them the same.
func TestEqualityDoesNotReadValuesAsNumbers(t *testing.T) {
	m := msg("0210", "39", "00")
	require.True(t, evalWhen(t, "field(39) == '00'", m))
	require.False(t, evalWhen(t, "field(39) == '0'", m), `"00" and "0" are different response codes`)
	// Ordering is the one place a value is read as a number, and there they are
	// equal -- which is why only ordering does it.
	require.True(t, evalWhen(t, "field(39) <= 0", m))
}

// A comparison against something the message does not carry is false, for every
// operator: a rule about a field's value has nothing to say when the field is
// not there.
func TestAComparisonNeverFiresOnWhatIsNotThere(t *testing.T) {
	empty := msg("0100")
	for _, src := range []string{
		"field(39) == '00'",
		"field(39) != '00'",
		"field(39) > 0",
		"field(39) < 99",
		"field(55.9F02) == '01'",
	} {
		require.False(t, evalWhen(t, src, empty), "%s fired on an absent field", src)
	}
	// present() is how absence is asked about, and it answers.
	require.False(t, evalWhen(t, "present(39)", empty))
	require.True(t, evalWhen(t, "present(39) == false", empty))
}

// The consequence of the rule above, pinned so nobody discovers it in
// production: negation is asymmetric across an absent field. Both readings are
// defensible and no total logic has neither.
func TestNegationIsAsymmetricAcrossAnAbsentField(t *testing.T) {
	empty := msg("0100")
	require.False(t, evalWhen(t, "field(39) != '00'", empty),
		"the comparison itself does not fire")
	require.True(t, evalWhen(t, "!(field(39) == '00')", empty),
		"negating a comparison that did not fire does")
}

// An empty field is present. "It is there and it is blank" and "it is not there"
// are different facts about a message, and a bitmap distinguishes them.
func TestAnEmptyValueIsStillPresent(t *testing.T) {
	m := msg("0100", "39", "")
	require.True(t, evalWhen(t, "present(39)", m))
	require.True(t, evalWhen(t, "field(39) == ''", m))
}

// Comparing a name to a number, or a boolean to a string, is false rather than
// an error: the language is total, so a spec someone else wrote cannot make a
// message fail to evaluate.
func TestNothingAnExpressionCanSayMakesEvaluationFail(t *testing.T) {
	m := msg("0100", "2", "4111111111111111", "43", "ACME STORE")
	for _, src := range []string{
		"field(43) > 10",
		"field(43) < 10",
		"present(43) == 'yes'",
		"mti > 'abc'",
	} {
		require.False(t, evalWhen(t, src, m), "%s should be false, not fatal", src)
	}
}

// Subfields address a path, which is what TLV and positional composites need.
func TestSubfieldPathsAreRead(t *testing.T) {
	m := msg("0200", "55.9F02", "000000010000", "3.1", "00")
	require.True(t, evalWhen(t, "field(55.9F02) == '000000010000'", m))
	require.True(t, evalWhen(t, "field(3.1) == '00'", m))
	require.False(t, evalWhen(t, "field(55.9F03) == '01'", m))
}

// Every `when` in the shipped spec parses and answers. A rule that cannot be
// evaluated is a rule that does nothing, and the spec is where that would show.
func TestEveryConditionInTheReferenceSpecEvaluates(t *testing.T) {
	spec, err := ParseSemantic(readRefSpec(t))
	require.NoError(t, err)

	m := msg("0200", "22", "05", "35", "track2", "39", "00", "4", "1000")
	var checked int
	for de, f := range spec.Fields {
		for _, r := range f.Messages {
			if strings.TrimSpace(r.When) == "" {
				continue
			}
			e, errP := ParseWhen(r.When)
			require.NoError(t, errP, "DE %d: %s", de, r.When)
			e.Eval(m) // must not panic; the answer depends on the message
			checked++
		}
	}
	require.Greater(t, checked, 3, "only %d conditions found; the spec should carry more", checked)
}
