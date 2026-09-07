// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

// The wire half every negative fixture shares. What is under test is the
// semantic layer on top, so the bytes stay constant and uninteresting.
const resolveWire = `spec:
  id: neg
  name: neg
  version: 1.0.0
  wire:
    fields:
      0: {type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
      2: {type: String, length: 19, enc: ASCII, prefix: ASCII.LL}
      3: {type: String, length: 6, enc: ASCII, prefix: ASCII.Fixed}
      4: {type: String, length: 12, enc: ASCII, prefix: ASCII.Fixed}
      39: {type: String, length: 2, enc: ASCII, prefix: ASCII.Fixed}
      49: {type: String, length: 3, enc: ASCII, prefix: ASCII.Fixed}
  enums:
    response_code:
      closed: false
      values:
        "00": {name: Approved, category: approved}
    currency_iso4217:
      closed: true
      values:
        "840": {name: USD}
    scheme_code:
      closed: false
      values:
        "00": {name: Approved}
  messages:
    catalog:
      "0200": {name: Request, flow: request, pairs_with: "0210"}
      "0210": {name: Response, flow: response, pairs_with: "0200"}
`

// A spec is mostly claims about itself, and until now none were checked: a claim
// that pointed at nothing loaded clean and quietly did nothing. Each of these is
// one such claim, and each must fail the whole load.
func TestSpecWithADanglingReferenceIsRejected(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"values_ref names an enum that is not declared": {
			body: "    39: {name: RC, values_ref: no_such_set}\n",
			want: `names the value set "no_such_set"`,
		},
		"@messages is the only special value set": {
			body: "    0: {name: MTI, values_ref: \"@catalog\"}\n",
			want: `the only special value set is "@messages"`,
		},
		"a when that does not parse": {
			body: "    39:\n      name: RC\n      messages:\n      - {mti: \"0210\", usage: mandatory, when: \"field(39 == (((\"}\n",
			want: "does not parse",
		},
		"a when reading a field the spec never declared": {
			body: "    39:\n      name: RC\n      messages:\n      - {mti: \"0210\", usage: mandatory, when: \"field(999) == '00'\"}\n",
			want: "reads field 999, which this spec does not declare",
		},
		"a when reading a part the composite does not have": {
			body: "    3:\n      name: PC\n      subfields:\n        layout: positional\n        parts:\n        - {name: Type}\n    39:\n      name: RC\n      messages:\n      - {mti: \"0210\", usage: mandatory, when: \"field(3.7) == '00'\"}\n",
			want: "declares no such part",
		},
		"a message rule naming a message outside the catalog": {
			body: "    39:\n      name: RC\n      messages:\n      - {mti: \"9999\", usage: mandatory}\n",
			want: `names message "9999", which is not in the catalog`,
		},
		"a response_value on a request": {
			body: "    39:\n      name: RC\n      messages:\n      - {mti: \"0200\", usage: mandatory, response_value: echo}\n",
			want: "no prior value for it to relate to",
		},
		"masking switched off on a classified field": {
			body: "    2: {name: PAN, sensitivity: pan, log_mask: false}\n",
			want: "cannot be switched off",
		},
		"a currency binding to a field that carries no currency": {
			body: "    4: {name: Amount, format: {kind: amount, currency_field: 39}}\n    39: {name: RC, values_ref: response_code}\n",
			want: "does not carry currency codes",
		},
		"a currency binding to a field that does not exist": {
			body: "    4: {name: Amount, format: {kind: amount, currency_field: 77}}\n",
			want: "field 77, which is not declared",
		},
		"a check asserting over an undeclared field": {
			body: "    0: {name: MTI}\n  checks:\n  - mti: [\"0200\"]\n    name: \"bad\"\n    assert: \"present(404)\"\n",
			want: "reads field 404",
		},
		"a generator drawing from a value set that is not declared": {
			body: "    0: {name: MTI}\n  x-fluxrig-simulation:\n    \"0200\":\n      3: {choose: {from: no_such_set}}\n",
			want: `draws from the value set "no_such_set"`,
		},
		"a generator weighting a value the set does not contain": {
			body: "    0: {name: MTI}\n  x-fluxrig-simulation:\n    \"0200\":\n      39: {choose: {from: response_code, weights: {\"99\": 5}}}\n",
			want: `weights the value "99"`,
		},
		"a traffic mix generating a message outside the catalog": {
			body: "    0: {name: MTI}\n  x-fluxrig-simulation:\n    mix:\n    - {use: \"9999\", weight: 1}\n",
			want: `generates "9999", which is not in the catalog`,
		},
		"a telemetry dimension that names nothing": {
			body: "    0: {name: MTI}\n  x-fluxrig-observability:\n    dimensions: [\"nowhere\"]\n",
			want: "names no field, alias or subfield",
		},
		"a telemetry dimension over a field with no value set": {
			body: "    39: {name: RC, alias: resp_code}\n  x-fluxrig-observability:\n    dimensions: [\"resp_code\"]\n",
			want: "no value set at all",
		},
		"a histogram over a field that does not exist": {
			body: "    4: {name: Amount}\n  x-fluxrig-observability:\n    histograms:\n    - {field: txn_total, by: cur, unit: minor_units}\n",
			want: "names no field or alias",
		},
		"a histogram grouped by something that is not its currency": {
			// The amount says which field carries its currency. Grouping by any
			// other adds up figures that were never comparable, and the total looks
			// entirely plausible.
			body: "    4: {name: Amount, alias: amt, format: {kind: amount, currency_field: 49}}\n    39: {name: RC, alias: rc}\n    49: {name: Currency, alias: cur, values_ref: currency_iso4217}\n  x-fluxrig-observability:\n    histograms:\n    - {field: amt, by: rc, unit: minor_units}\n",
			want: "adds up figures in different currencies",
		},
		"a telemetry dimension over an open, uncategorised value set": {
			// The set is open, so a scheme may send a code nobody listed — and with
			// no category there is no bucket for it, which is a time series per
			// unknown value.
			body: "    39: {name: RC, alias: resp_code, values_ref: scheme_code}\n  x-fluxrig-observability:\n    dimensions: [\"resp_code\"]\n",
			want: "no `category`",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "spec.yaml")
			body := resolveWire + "  fields:\n" + tc.body
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := LoadSpec(p)
			if err == nil {
				t.Fatal("the spec loaded clean")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error should say what is wrong.\n  want substring: %s\n  got: %v", tc.want, err)
			}
		})
	}
}

// Conditions that depend on one another in a loop never settle, and generation
// resolves them to a fixpoint.
func TestCyclicConditionsAreRejected(t *testing.T) {
	body := resolveWire + `  fields:
    2:
      name: PAN
      messages:
      - {mti: "0200", usage: mandatory, when: "present(39)"}
    39:
      name: RC
      messages:
      - {mti: "0200", usage: mandatory, when: "present(2)"}
`
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadSpec(p)
	if err == nil {
		t.Fatal("a cycle between two conditions was accepted")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error should name the cycle: %v", err)
	}
}

// Everything an author got wrong should arrive in one load, not one per fix.
func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	body := resolveWire + `  fields:
    2: {name: PAN, sensitivity: pan, log_mask: false}
    39: {name: RC, values_ref: nope}
    4:
      name: Amount
      messages:
      - {mti: "0200", usage: mandatory, response_value: echo}
`
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadSpec(p)
	if err == nil {
		t.Fatal("three problems loaded clean")
	}
	for _, want := range []string{"cannot be switched off", `value set "nope"`, "response_value"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("one load should report every problem; missing %q in:\n%v", want, err)
		}
	}
}

// The resolver is only worth what it is pointed at. Every other fixture in this
// repository carries names and aliases and nothing else, so loading them proves
// the resolver does not break what already worked — not that it works.
//
// The Conductor suite's spec is the one fixture with a semantic layer that
// describes what its own suite does: the two request/response pairs its tools
// exchange, the three dispositions its scheme hosts stamp, and which fields are
// echoed rather than originated. This asserts that content is still there, so it
// cannot be quietly stripped back to a wire-only file and leave the suite
// passing over nothing.
func TestTheConductorFixtureHasSomethingToResolve(t *testing.T) {
	const path = "../../../../../../test/robot/suites/conductor/specs/auth.yaml"

	if _, _, err := LoadSpec(path); err != nil {
		t.Fatalf("the fixture does not resolve: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc specDoc
	if err := yamlUnmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	s := doc.Spec

	if len(s.Messages.Catalog) < 4 {
		t.Errorf("the catalog has %d messages; the suite exchanges 0100/0110 and 0200/0210", len(s.Messages.Catalog))
	}
	if _, ok := s.Enums["response_code"]; !ok {
		t.Error("no response_code value set; the scheme hosts stamp 00, 05 and 91")
	}
	withMatrix, withEcho, withNew := 0, 0, 0
	for _, f := range s.Fields {
		if len(f.Messages) > 0 {
			withMatrix++
		}
		for _, e := range f.Messages {
			switch e.ResponseValue {
			case "echo":
				withEcho++
			case "new":
				withNew++
			}
		}
	}
	if withMatrix < 5 {
		t.Errorf("only %d fields carry a per-message matrix; the resolver has almost nothing to check", withMatrix)
	}
	if withEcho == 0 || withNew == 0 {
		t.Errorf("provenance is what a request/response pair is for: echo=%d new=%d", withEcho, withNew)
	}
}

// yamlUnmarshal keeps the test reading the document the same way the resolver
// does, rather than through a second parser that could disagree with it.
func yamlUnmarshal(data []byte, v any) error { return yaml.Unmarshal(data, v) }

// `description` belongs to the wire layer, where it is Moov's word for an
// element's label. Someone arriving from upstream writes the key they know; the
// loader now catches it as a layer violation and says which key they wanted,
// rather than accepting it and rendering their label as body text.
func TestADescriptionInTheSemanticLayerIsRefused(t *testing.T) {
	_, _, err := LoadSpecContent([]byte(`
spec:
  id: from-upstream
  name: From Upstream
  version: "1.0.0"
  wire:
    format: moov
    source: "moov:spec87ascii"
  fields:
    2:
      description: Primary Account Number
`), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "field 2")
	require.Contains(t, err.Error(), "`name`")
	require.Contains(t, err.Error(), "`meaning`")
}

// A field with neither is fine. Not every element in a spec is documented, and
// an entry that exists only to carry an alias is a legitimate thing to write.
func TestAFieldWithNeitherNameNorDescriptionIsFine(t *testing.T) {
	_, _, err := LoadSpecContent([]byte(`
spec:
  id: sparse
  name: Sparse
  version: "1.0.0"
  wire:
    format: moov
    source: "moov:spec87ascii"
  fields:
    2:
      alias: pan
`), "")
	require.NoError(t, err)
}
