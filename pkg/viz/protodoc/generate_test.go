// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

const refSpec = "../../../examples/specs/iso8583-v87-ascii.yaml"

func reference(t *testing.T) *sdl.Spec {
	t.Helper()
	raw, err := os.ReadFile(refSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := sdl.ParseSemantic(raw)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// A private field is a proprietary extension, and `scope` is where a spec says
// so. If that marking does not hold, the public variant leaks the thing the
// marking exists to withhold — silently, because a document that is missing a
// section looks exactly like one that never had it.
func TestThePublicVariantWithholdsPrivateFields(t *testing.T) {
	spec := reference(t)

	var private []string
	for de, f := range spec.Fields {
		if !f.IsPublic() {
			private = append(private, headingFor(de, f))
		}
	}
	if len(private) == 0 {
		t.Fatal("the reference spec marks no field private, so this proves nothing")
	}

	pub := Markdown(spec, Options{Scope: ScopePublic})
	full := Markdown(spec, Options{Scope: ScopeComplete})
	for _, heading := range private {
		if strings.Contains(pub, heading) {
			t.Errorf("the public variant carries %q", heading)
		}
		if !strings.Contains(full, heading) {
			t.Errorf("the complete variant is missing %q", heading)
		}
	}
	// Withholding silently is its own defect: the reader must know something was.
	if !strings.Contains(pub, "private elements are omitted") {
		t.Error("the public variant does not say that anything was withheld")
	}
}

// A private field must not reach the public variant through a message table
// either. Withholding a field's own section while listing it in the per-message
// view would publish its number, its name, and where it appears.
func TestAPrivateFieldIsAbsentFromTheMessageTablesToo(t *testing.T) {
	spec := reference(t)
	pub := Markdown(spec, Options{Scope: ScopePublic})

	inMessages := pub[:strings.Index(pub, "## Data elements")]
	for de, f := range spec.Fields {
		if f.IsPublic() || f.Name == "" {
			continue
		}
		if strings.Contains(inMessages, f.Name) {
			t.Errorf("DE %d is private and its name appears in a message table", de)
		}
	}
}

// The per-message view is the field-major matrix inverted, and inversion is
// where a view stops agreeing with its source. Every rule in the spec must reach
// the message it names.
func TestEveryRuleReachesItsMessageTable(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	sections := map[string]string{}
	for mti := range spec.Messages.Catalog {
		start := strings.Index(out, "### "+mti+" — ")
		if start < 0 {
			t.Fatalf("message %s has no section", mti)
		}
		rest := out[start+4:]
		end := strings.Index(rest, "\n### ")
		if end < 0 {
			end = len(rest)
		}
		sections[mti] = rest[:end]
	}

	checked := 0
	for de, f := range spec.Fields {
		for _, rule := range f.Messages {
			for _, mti := range rule.Codes() {
				body, ok := sections[mti]
				if !ok {
					continue
				}
				row := "| " + itoa(de) + " | "
				if !strings.Contains(body, row) {
					t.Errorf("DE %d has a rule for %s and no row in that message's table", de, mti)
				}
				checked++
			}
		}
	}
	if checked < 30 {
		t.Errorf("only %d rules were checked; the reference spec should exercise more", checked)
	}
}

// A `when` carries `||` for nearly every disjunction, and a pipe ends a table
// cell wherever it appears. An unescaped condition shears its own row in half,
// and the table still renders — just wrongly.
func TestConditionsDoNotBreakTheTables(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	found := false
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "|") || !strings.Contains(line, "field(") {
			continue
		}
		if strings.Contains(line, "||") && !strings.Contains(line, `\|\|`) {
			t.Errorf("a condition's pipes are unescaped, so this row breaks:\n  %s", line)
		}
		if strings.Contains(line, `\|\|`) {
			found = true
		}
	}
	if !found {
		t.Error("no escaped condition was rendered; this test is checking nothing")
	}
}

// Whether a value outside a set is invalid or merely undocumented is the whole
// point of `closed`, and a reference that does not say which leaves the reader
// to guess.
func TestValueTablesSayWhetherTheSetIsClosed(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	if !strings.Contains(out, "this list is complete; anything else is invalid") {
		t.Error("no closed value set is described as closed")
	}
	if !strings.Contains(out, "the documented set. Others may occur") {
		t.Error("no open value set is described as open")
	}
	// Categories are the analytics axis; a table that drops them loses the only
	// grouping a reader can chart by.
	if !strings.Contains(out, "| Group |") {
		t.Error("categories are not rendered")
	}
}

func headingFor(de int, f sdl.Field) string {
	return "### DE " + itoa(de) + " — " + f.Name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A field's value domain can differ by message: the processing codes an
// authorization admits are not the ones a reversal admits, and `000000` can mean
// "goods and services" in one and "reverse a purchase" in the other. The model
// carries that on the message rule, and a reference that renders only the
// field-level set shows a field with no values at all — which is what it did.
func TestPerMessageValueDomainsAreRendered(t *testing.T) {
	spec, err := sdl.ParseSemantic([]byte(`
spec:
  id: pv
  name: pv
  version: 1.0.0
  enums:
    proc_auth:
      closed: true
      values:
        "000000": {name: Goods and services}
    proc_reversal:
      closed: true
      values:
        "000000": {name: Reverse a purchase}
    fallback:
      values:
        "999999": {name: Anything else}
  messages:
    catalog:
      "0100": {name: Auth, flow: request}
      "0400": {name: Reversal, flow: request}
  fields:
    3:
      name: Processing Code
      values_ref: fallback
      messages:
      - {mti: "0100", usage: mandatory, values_ref: proc_auth}
      - {mti: "0400", usage: mandatory, values_ref: proc_reversal}
`))
	if err != nil {
		t.Fatal(err)
	}
	out := Markdown(spec, Options{Scope: ScopeComplete})

	for _, want := range []string{
		"**Values in 0100**",
		"**Values in 0400**",
		"Goods and services",
		"Reverse a purchase",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the reference does not show %q", want)
		}
	}
	// The field-level set still holds where nothing narrows it, and saying so is
	// the difference between a default and a contradiction.
	if !strings.Contains(out, "where no message narrows them") {
		t.Error("the field-level set is not marked as the fallback")
	}
	// And the rule table has to name which domain applies, or the tables below it
	// are unattached to anything.
	if !strings.Contains(out, "| `proc_auth` |") {
		t.Error("the message rule table does not name the domain that applies")
	}
}

// The message table answers "what does an 0100 carry". An element with three
// conditional rules appearing as three rows reads as three contradictory
// answers — and on a narrow screen the condition that reconciles them is off to
// the right, so it reads as contradiction and nothing else.
func TestAMessageTableHasOneRowPerElement(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	// DE 55 carries three rules in an 0100: mandatory for chip, forbidden for
	// magnetic stripe, optional otherwise.
	var rules int
	for _, rule := range spec.Fields[55].Messages {
		for _, mti := range rule.Codes() {
			if mti == "0100" {
				rules++
			}
		}
	}
	if rules < 3 {
		t.Fatalf("DE 55 has %d rules in an 0100; this test needs the conditional case", rules)
	}

	section := messageSection(t, out, "0100")
	if got := strings.Count(section, "\n| 55 |"); got != 1 {
		t.Errorf("DE 55 has %d rows in the 0100 table, want 1", got)
	}
	// Folding must not lose what it folded.
	for _, want := range []string{"mandatory when", "forbidden when", "optional otherwise"} {
		if !strings.Contains(section, want) {
			t.Errorf("the folded row does not say %q", want)
		}
	}
	if !strings.Contains(section, "| conditional |") {
		t.Error("an element whose usage depends on the message's contents is not marked as such")
	}
}

func messageSection(t *testing.T, out, mti string) string {
	t.Helper()
	start := strings.Index(out, "### "+mti+" — ")
	if start < 0 {
		t.Fatalf("no section for %s", mti)
	}
	rest := out[start+4:]
	if end := strings.Index(rest, "\n### "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// A column every row answers with a dash is not neutral: it pushes the columns
// that identify a row off a narrow screen, so a reader scrolls to five
// "mandatory" chips with no way to tell which element each belongs to. A request
// has no response value by definition — the loader rejects one — so that column
// does not belong on a request's table at all.
func TestAMessageTableOmitsColumnsThatSayNothing(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	var sawRequest, sawResponse bool
	for mti, m := range spec.Messages.Catalog {
		header := tableHeader(t, messageSection(t, out, mti))
		switch m.Flow {
		case "request":
			sawRequest = true
			if strings.Contains(header, "Response value") {
				t.Errorf("%s is a request and its table carries a response column: %s", mti, header)
			}
		case "response":
			sawResponse = true
		}
		if !strings.Contains(header, "Element") || !strings.Contains(header, "Name") {
			t.Errorf("%s must always identify its rows: %s", mti, header)
		}
	}
	if !sawRequest || !sawResponse {
		t.Fatal("the reference spec should have both a request and a response to compare")
	}

	// The column has to appear where it means something, or this is just deletion.
	if !strings.Contains(tableHeader(t, messageSection(t, out, "0110")), "Response value") {
		t.Error("an authorization response does not carry the response column")
	}
}

func tableHeader(t *testing.T, section string) string {
	t.Helper()
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| Element ") {
			return line
		}
	}
	return ""
}

// A field's note is a different kind of statement from its description: the
// description defines the element, the note says what will bite you about it.
// Running them together as one paragraph buries the second, which is the half a
// reader most needs.
func TestFieldNotesAreSetApartFromDescriptions(t *testing.T) {
	spec := reference(t)
	out := Markdown(spec, Options{Scope: ScopeComplete})

	noted := 0
	for de, f := range spec.Fields {
		if f.Note == "" {
			continue
		}
		noted++
		if !strings.Contains(out, "> "+f.Note) {
			t.Errorf("DE %d's note is not set apart from its prose", de)
		}
		if f.Meaning != "" && strings.Contains(out, f.Meaning+" "+f.Note) {
			t.Errorf("DE %d runs its description and note together", de)
		}
	}
	if noted == 0 {
		t.Fatal("no field in the reference spec carries a note, so this proves nothing")
	}
}

// An element's label comes from the wire layer, and the semantic `name`
// overrides it. Before this, the label was stripped out of the wire document as
// if it were a semantic key, so a spec had to restate thirty-four labels that
// already existed: the same string in two places, drifting apart the first time
// one was corrected.
func TestALabelComesFromTheWireLayerUnlessOverridden(t *testing.T) {
	raw, err := os.ReadFile(refSpec)
	require.NoError(t, err)
	spec, err := sdl.ParseSemantic(raw)
	require.NoError(t, err)

	doc := Build(spec, Options{Scope: ScopeComplete, Source: raw, BaseDir: filepath.Dir(refSpec)})

	byDE := map[int]FieldView{}
	for _, f := range doc.FieldViews {
		byDE[f.DE] = f
	}

	// Inherited: the spec says nothing about what DE 2 is called.
	require.Empty(t, spec.Fields[2].Name, "the test's premise is gone: DE 2 now declares a name")
	require.Equal(t, "Primary Account Number", byDE[2].Name)

	// Overridden: moov calls DE 4 "Transaction Amount"; this protocol does not.
	require.Equal(t, "Amount, Transaction", spec.Fields[4].Name)
	require.Equal(t, "Amount, Transaction", byDE[4].Name)

	// Nothing is left nameless, in either scope.
	for _, f := range doc.FieldViews {
		require.NotEmpty(t, f.Name, "DE %d has no label", f.DE)
	}

	// An element the wire layer declares and the semantic layer says nothing
	// about is still documented. A reference with holes in it is what the
	// placeholder entries used to be avoiding.
	require.Contains(t, byDE, 18)
	require.NotContains(t, spec.Fields, 18)

	// And the heading counts what the body shows.
	require.Equal(t, len(doc.FieldViews), doc.Elements)
}
