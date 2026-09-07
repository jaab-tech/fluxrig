// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"sort"
	"strings"
)

// Violation is one rule a message broke.
type Violation struct {
	// Severity is reject or warn. A rejection fails the message; a warning is
	// recorded and the message goes on, which is what lets a rule reach a live
	// fleet before it is enforced on one.
	Severity string
	// Kind says which rule: usage, value or check.
	Kind string
	// DE is the data element the violation is about, or 0 for a check.
	DE int
	// Path is how the field was addressed, so a subfield reads as "55.9F02".
	Path string
	// Rule names the check, or is empty for the matrix rules.
	Rule string
	// Reason is what to tell whoever reads the log.
	Reason string
}

func (v Violation) String() string {
	if v.DE > 0 {
		return fmt.Sprintf("DE %s: %s", v.Path, v.Reason)
	}
	return fmt.Sprintf("%s: %s", v.Rule, v.Reason)
}

// Kinds of rule a message can break.
const (
	ViolationUsage = "usage"
	ViolationValue = "value"
	ViolationCheck = "check"
)

// Validator applies a spec's semantic rules to messages.
//
// It is compiled once from a spec and then read-only, so it is safe to share
// across the goroutines processing traffic. Every expression is parsed here;
// nothing is parsed per message.
type Validator struct {
	// byMTI holds the field rules that can apply to each message type, in the
	// order the spec wrote them -- the first entry whose condition holds decides
	// the field's usage.
	byMTI map[string][]compiledRule
	// checks are the cross-field assertions, per message type.
	checks map[string][]compiledCheck
	// closed holds the value sets a field's value must belong to. An open set
	// lists what is known and permits the rest, so only closed ones are rules.
	closed map[string]*ValueSet
	// paths remembers how each field is addressed, for the messages.
	paths map[int]string
}

type compiledRule struct {
	de     int
	path   string
	usage  string
	when   *WhenExpr
	values *ValueSet
}

type compiledCheck struct {
	name     string
	severity string
	assert   *WhenExpr
	note     string
}

// NewValidator compiles a spec's semantic rules.
//
// It fails only on an expression that does not parse, which the loader has
// already refused -- so a spec that loaded compiles here. Failing anyway rather
// than skipping the rule is deliberate: a validator that silently dropped what
// it could not read would report a clean message it never checked.
func NewValidator(spec *Spec) (*Validator, error) {
	// How an element compares is the spec's to say, and it says it with
	// `format.kind`. Without this every rule would compare characters, so a rule
	// on an amount would have to write the element's own zero padding into
	// itself.
	kindOf := func(ref FieldRef) string {
		f, ok := spec.Fields[ref.DE]
		if !ok {
			return ""
		}
		// Only an element declares a kind. A part of a composite has a name and
		// a value domain and no `format`, so a rule on a subfield compares
		// characters -- which is what a tag's value usually is.
		if len(ref.Subs) > 0 {
			return ""
		}
		return f.Format.Kind
	}

	v := &Validator{
		byMTI:  map[string][]compiledRule{},
		checks: map[string][]compiledCheck{},
		closed: map[string]*ValueSet{},
		paths:  map[int]string{},
	}

	des := make([]int, 0, len(spec.Fields))
	for de := range spec.Fields {
		des = append(des, de)
	}
	// The rules are walked in field order so a message's violations come back in
	// an order a person can scan, rather than in map order.
	sort.Ints(des)

	for _, de := range des {
		f := spec.Fields[de]
		path := fmt.Sprint(de)
		v.paths[de] = path

		if set := valueSetFor(spec, f.ValuesRef, f.ValidValues); set != nil && set.Closed {
			v.closed[path] = set
		}

		for _, r := range f.Messages {
			var when *WhenExpr
			if strings.TrimSpace(r.When) != "" {
				parsed, err := ParseWhen(r.When)
				if err != nil {
					return nil, fmt.Errorf("DE %d: %w", de, err)
				}
				parsed.BindKinds(kindOf)
				when = parsed
			}
			rule := compiledRule{
				de:     de,
				path:   path,
				usage:  r.Usage,
				when:   when,
				values: valueSetFor(spec, r.ValuesRef, r.ValidValues),
			}
			for _, code := range r.Codes() {
				v.byMTI[code] = append(v.byMTI[code], rule)
			}
		}
	}

	for i, c := range spec.Checks {
		parsed, err := ParseWhen(c.Assert)
		if err != nil {
			name := c.Name
			if name == "" {
				name = fmt.Sprintf("check %d", i+1)
			}
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		parsed.BindKinds(kindOf)
		cc := compiledCheck{name: c.Name, severity: c.Level(), assert: parsed, note: c.Note}
		if cc.name == "" {
			cc.name = fmt.Sprintf("check %d", i+1)
		}
		for _, code := range c.MTI {
			v.checks[code] = append(v.checks[code], cc)
		}
	}
	return v, nil
}

// valueSetFor resolves whichever way a rule named its values: by reference to a
// shared set, or by writing one inline.
func valueSetFor(spec *Spec, ref string, inline *ValueSet) *ValueSet {
	if inline != nil {
		return inline
	}
	if ref == "" {
		return nil
	}
	return spec.Enums[ref]
}

// Validate reports every rule the message breaks, in field order.
//
// It reports all of them rather than stopping at the first: whoever is looking
// at a rejected message wants to know what is wrong with it, not the first thing
// that happened to be checked.
func (v *Validator) Validate(m Subject) []Violation {
	if v == nil {
		return nil
	}
	mti := m.MTI()
	var out []Violation

	for _, r := range v.byMTI[mti] {
		// A rule with a condition applies only where the condition holds, and a
		// rule with none always applies.
		if r.when != nil && !r.when.Eval(m) {
			continue
		}
		value, present := m.Field(r.de, nil)

		switch r.usage {
		case "mandatory":
			if !present {
				out = append(out, Violation{
					Severity: SeverityReject, Kind: ViolationUsage, DE: r.de, Path: r.path,
					Reason: v.becauseOf(r, "is mandatory and is not present"),
				})
				continue
			}
		case "forbidden":
			if present {
				out = append(out, Violation{
					Severity: SeverityReject, Kind: ViolationUsage, DE: r.de, Path: r.path,
					Reason: v.becauseOf(r, "must not be present and is"),
				})
			}
			continue
		}

		if !present {
			continue
		}
		// A closed set is a claim that those are all the values there are. An
		// open one lists what is known and permits the rest, so it is not a rule.
		set := r.values
		if set == nil {
			set = v.closed[r.path]
		}
		if set == nil || !set.Closed {
			continue
		}
		if _, ok := set.Values[value]; !ok {
			out = append(out, Violation{
				Severity: SeverityReject, Kind: ViolationValue, DE: r.de, Path: r.path,
				Reason: fmt.Sprintf("carries %q, which its value set does not list (the set is closed, so %s)",
					value, known(set)),
			})
		}
	}

	for _, c := range v.checks[mti] {
		if c.assert.Eval(m) {
			continue
		}
		reason := fmt.Sprintf("%q does not hold", c.assert.Source)
		if c.note != "" {
			reason += ". " + c.note
		}
		out = append(out, Violation{
			Severity: c.severity, Kind: ViolationCheck, Rule: c.name, Reason: reason,
		})
	}
	return out
}

// becauseOf says what made a conditional rule apply. A message rejected for a
// rule that only fires sometimes is unreadable without the condition.
func (v *Validator) becauseOf(r compiledRule, what string) string {
	if r.when == nil {
		return what
	}
	return fmt.Sprintf("%s when %s", what, r.when.Source)
}

// known renders a closed set's values, capped: a set of two hundred currency
// codes belongs in the reference, not in a log line.
func known(set *ValueSet) string {
	codes := make([]string, 0, len(set.Values))
	for c := range set.Values {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	const most = 8
	if len(codes) > most {
		return fmt.Sprintf("one of %d listed values", len(codes))
	}
	return "expected one of " + strings.Join(codes, ", ")
}

// Rejects reports whether any violation fails the message.
func Rejects(vs []Violation) bool {
	for _, v := range vs {
		if v.Severity == SeverityReject {
			return true
		}
	}
	return false
}
