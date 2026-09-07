// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"strings"
	"testing"
)

// The expressions the reference spec and the ADR actually use. If the parser
// cannot read these, nothing downstream matters.
func TestWhenAcceptsTheExpressionsSpecsWrite(t *testing.T) {
	for _, src := range []string{
		"field(39) == '00'",
		"present(35) == false",
		"field(22) == '05' || field(22) == '07'",
		"field(4) > 1000000",
		"mti == '0410'",
		"!present(55)",
		"present(2) || present(35)",
		"field(55.9F02) == '000000001000'",
		"field(3.1) == '00'",
		"(field(39) == '00' || field(39) == '10') && present(38)",
		"true",
		"field(14) < '2501'",
	} {
		if _, err := ParseWhen(src); err != nil {
			t.Errorf("%q should parse: %v", src, err)
		}
	}
}

// Every referenced location has to be recoverable, because that is what the
// resolver checks against the catalog and what cycles are computed from.
func TestWhenReportsWhatItReads(t *testing.T) {
	e, err := ParseWhen("field(55.9F02) == '01' && present(3.1) || mti == '0200'")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(e.Refs))
	for _, r := range e.Refs {
		got = append(got, r.String())
	}
	if strings.Join(got, ",") != "55.9F02,3.1" {
		t.Errorf("read %v, want [55.9F02 3.1]", got)
	}
	// `mti` is not a field reference and must not be reported as one.
	for _, r := range e.Refs {
		if r.DE == 0 {
			t.Errorf("mti was recorded as a field reference: %+v", r)
		}
	}
}

// A malformed expression must be rejected with something an author can act on:
// what is wrong, and where.
func TestWhenRejectsAndSaysWhere(t *testing.T) {
	for src, want := range map[string]string{
		"field(39 == '00'":          "expected ')'",
		"field(39) = '00'":          "written ==",
		"field(39) == '00":          "never closed",
		"field(39) == '00' & x":     "&& and ||",
		"field(39)":                 "not a condition on its own",
		"field(abc) == '1'":         "not a data element number",
		"field(55.) == '1'":         "empty",
		"frobnicate(39) == '1'":     "unknown name",
		"field(39) == '00' garbage": "after the end of the expression",
		"":                          "expected a value",
	} {
		_, err := ParseWhen(src)
		if err == nil {
			t.Errorf("%q should be rejected", src)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error should mention %q, got: %v", src, want, err)
		}
		if !strings.Contains(err.Error(), "position") && src != "" {
			t.Errorf("%q: error should say where: %v", src, err)
		}
	}
}

// A pathological spec must not reach the stack limit; the depth bound is part of
// the contract.
func TestWhenBoundsNesting(t *testing.T) {
	deep := strings.Repeat("(", 200) + "field(39) == '00'" + strings.Repeat(")", 200)
	_, err := ParseWhen(deep)
	if err == nil {
		t.Fatal("a 200-deep expression was accepted")
	}
	if !strings.Contains(err.Error(), "nests deeper") {
		t.Errorf("the error should name the limit: %v", err)
	}
}
