// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The reference resolver: every cross-reference a spec makes, checked once at
// load time.
//
// A spec is mostly claims about itself — this field's values come from that enum,
// this message pairs with that one, this condition reads that data element. None
// of them were checked, so a spec naming an enum it never declared loaded clean
// and the reference simply did nothing. That is the failure this whole layer is
// prone to: not a crash, a silence.
//
// Everything here runs at load and fails the spec as a whole. A malformed spec
// must never become a per-transaction error: a Rack that accepted the spec has
// already told the Mixer it is serving that protocol.

// resolveErrors accumulates every problem rather than stopping at the first, so
// one load reports everything an author has to fix.
type resolveErrors []string

func (e resolveErrors) err() error {
	if len(e) == 0 {
		return nil
	}
	if len(e) == 1 {
		return fmt.Errorf("spec is not internally consistent: %s", e[0])
	}
	return fmt.Errorf("spec is not internally consistent:\n  - %s", strings.Join(e, "\n  - "))
}

// specDoc is the semantic layer as the resolver reads it: everything that can
// name something else.
type specDoc struct {
	Spec Spec `yaml:"spec"`
}

// ParseSemantic reads a spec's semantic layer.
//
// It is exported so a renderer — the protocol reference, a value table, a
// per-message view — reads the vocabulary through the type that defines it. A
// second parser over the same document is a second opinion about what the
// document means, and the two drift apart in the direction nobody is testing.
//
// It does not resolve references. LoadSpec does that, and a document that
// reached a renderer has already been through it.
func ParseSemantic(data []byte) (*Spec, error) {
	var doc specDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse semantic layer: %w", err)
	}
	return &doc.Spec, nil
}

// Reference is one document a spec was written from.
type Reference struct {
	Title     string `yaml:"title"`
	Publisher string `yaml:"publisher"`
	// Note says how a reader obtains it, which for a paid standard or a scheme
	// manual is the part they actually need.
	Note string `yaml:"note"`
	URL  string `yaml:"url"`
}

type Message struct {
	Name        string   `yaml:"name"`
	Meaning     string   `yaml:"meaning"`
	Flow        string   `yaml:"flow"`
	PairsWith   string   `yaml:"pairs_with"`
	Transitions []string `yaml:"transitions"`
}

type Part struct {
	Tag         string    `yaml:"tag"`
	Name        string    `yaml:"name"`
	Meaning     string    `yaml:"meaning"`
	ValuesRef   string    `yaml:"values_ref"`
	ValidValues *ValueSet `yaml:"validValues"`
}

// ValueSet is a value domain. `closed` is the difference between "a value outside
// this list is invalid" and "this list is what is documented" — two claims a bare
// list cannot tell apart, and the one a validator has to know before it can act.
type ValueSet struct {
	Closed bool `yaml:"closed"`
	Values map[string]struct {
		Name     string `yaml:"name"`
		Meaning  string `yaml:"meaning"`
		Category string `yaml:"category"`
	} `yaml:"values"`
}

// categorised reports whether every value carries a category. Categories are what
// let a reader group values that were never listed: without them, an open set has
// no bucket for what it did not anticipate.
func (v *ValueSet) categorised() bool {
	if v == nil || len(v.Values) == 0 {
		return false
	}
	for _, entry := range v.Values {
		if entry.Category == "" {
			return false
		}
	}
	return true
}

// MessageRule is what a field means in one message. Two independent
// axes live here and must not be confused: `usage` answers whether the field is
// there at all, and `response_value` answers how its value relates to the
// request's.
type MessageRule struct {
	MTI           any       `yaml:"mti"`
	Usage         string    `yaml:"usage"`
	When          string    `yaml:"when"`
	ResponseValue string    `yaml:"response_value"`
	ValuesRef     string    `yaml:"values_ref"`
	ValidValues   *ValueSet `yaml:"validValues"`
	Note          string    `yaml:"note"`
}

// Codes lists the messages a rule applies to, however the document wrote them:
// one MTI or several.
func (r MessageRule) Codes() []string { return mtiCodes(r.MTI) }

// IsPublic reports whether a field may appear in published documentation. Scope
// defaults to public, so a field is private only by saying so.
func (f Field) IsPublic() bool { return f.Scope != "private" }

type Field struct {
	Name    string `yaml:"name"`
	Alias   string `yaml:"alias"`
	Meaning string `yaml:"meaning"`
	// Note is what a reader has to know that is not the definition: a caveat, a
	// consequence, a mistake that gets made. Meaning says what the element is;
	// a note says what will bite you about it.
	Note        string    `yaml:"note"`
	Scope       string    `yaml:"scope"`
	Sensitivity string    `yaml:"sensitivity"`
	LogMask     *bool     `yaml:"log_mask"`
	ValuesRef   string    `yaml:"values_ref"`
	ValidValues *ValueSet `yaml:"validValues"`
	Format      struct {
		Kind          string `yaml:"kind"`
		CurrencyField *int   `yaml:"currency_field"`
	} `yaml:"format"`
	Subfields struct {
		Layout string `yaml:"layout"`
		Parts  []Part `yaml:"parts"`
	} `yaml:"subfields"`
	Messages []MessageRule `yaml:"messages"`
}

type Check struct {
	MTI    []string `yaml:"mti"`
	Name   string   `yaml:"name"`
	Assert string   `yaml:"assert"`
	// Severity decides what a failure does. `reject` fails the message;
	// `warn` records it and lets it through, which is what makes a rule
	// deployable to a live fleet before it is enforced on one.
	//
	// The reference spec has written both since it was migrated; nothing read
	// them, so every check was equally unenforced.
	Severity string `yaml:"severity"`
	Note     string `yaml:"note"`
}

// Severity levels a rule can carry.
const (
	SeverityReject = "reject"
	SeverityWarn   = "warn"
)

// Level is the severity to act on, defaulting to reject. A check with no
// severity is a rule someone wrote to be obeyed; warning is the deliberate,
// stated choice.
func (c Check) Level() string {
	if c.Severity == SeverityWarn {
		return SeverityWarn
	}
	return SeverityReject
}

type Spec struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	// Overview is the protocol's own primer, written by whoever wrote the spec.
	// A generated reference explains the vocabulary it uses, and cannot explain
	// the protocol: what an MTI is, how a bitmap says which elements are
	// present, why an amount is in minor units. Without somewhere to put that,
	// a reader has to go and find a standards document — and for this protocol
	// those are sold, or circulate under a scheme's terms.
	Overview string `yaml:"overview"`
	// Wire names where the byte layout comes from. It is not in the document —
	// which is the point of naming a base — so a reader shown "everything is
	// derived from this file" is being told half the truth.
	Wire struct {
		Source string `yaml:"source"`
	} `yaml:"wire"`
	// References are the documents this spec was written from. A protocol
	// reference that cannot be traced to a normative source is an assertion; and
	// the sources for this protocol are sold by a standards body or issued under
	// a scheme's terms, so a spec cites them rather than carrying them.
	References []Reference `yaml:"references"`
	Messages   struct {
		Catalog map[string]Message `yaml:"catalog"`
	} `yaml:"messages"`
	Enums         map[string]*ValueSet `yaml:"enums"`
	Fields        map[int]Field        `yaml:"fields"`
	Checks        []Check              `yaml:"checks"`
	Simulation    simulation           `yaml:"x-fluxrig-simulation"`
	Observability struct {
		Dimensions []string `yaml:"dimensions"`
		Histograms []struct {
			Field string `yaml:"field"`
			By    string `yaml:"by"`
			Unit  string `yaml:"unit"`
		} `yaml:"histograms"`
	} `yaml:"x-fluxrig-observability"`
}

// simulation is the generator's section. Its per-message templates sit beside
// its own keys rather than under one, so they are picked out by shape: a
// four-digit key is a message, and anything else belongs to the section itself.
type simulation struct {
	Mix []struct {
		Use string `yaml:"use"`
	}
	Defaults  map[int]simValue
	Templates map[string]map[int]simValue
}

func (s *simulation) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	s.Templates = map[string]map[int]simValue{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		switch {
		case key == "mix":
			_ = val.Decode(&s.Mix)
		case key == "defaults":
			_ = val.Decode(&s.Defaults)
		case len(key) == 4 && isAllDigits(key):
			tmpl := map[int]simValue{}
			if err := val.Decode(&tmpl); err == nil {
				s.Templates[key] = tmpl
			}
		}
	}
	return nil
}

// simValue is what a generator puts in a field: a literal, a macro, or a choice.
// Only a choice can name something else, and naming something else is what has
// to be checked.
type simValue struct {
	Choose struct {
		From    string         `yaml:"from"`
		Weights map[string]int `yaml:"weights"`
	} `yaml:"choose"`
}

// UnmarshalYAML tolerates the common case. Most values are a literal or a macro
// — a scalar — and a type that only accepted the richer form would refuse every
// spec that never needed it.
func (v *simValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	type raw simValue
	var out raw
	if err := node.Decode(&out); err != nil {
		return nil // a shape this checker does not read is not a failure to load
	}
	*v = simValue(out)
	return nil
}

// currencyEnum is the value set a `format.currency_field` must point into. A
// currency binding that names a field carrying something else makes every
// amount comparison and every generated histogram wrong in a way no test at the
// far end would attribute to the spec.
const currencyEnum = "currency_iso4217"

// resolveReferences checks every cross-reference in the semantic layer.
func resolveReferences(doc *specDoc) error {
	var errs resolveErrors
	s := &doc.Spec

	fieldExists := func(de int) bool { _, ok := s.Fields[de]; return ok }
	subExists := func(de int, subs []string) bool {
		f, ok := s.Fields[de]
		if !ok {
			return false
		}
		for _, want := range subs {
			found := false
			for i, p := range f.Subfields.Parts {
				// TLV parts are addressed by tag, positional ones by ordinal.
				if p.Tag == want || (p.Tag == "" && fmt.Sprint(i+1) == want) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return len(subs) == 0 || len(f.Subfields.Parts) > 0
	}
	enumExists := func(name string) bool { _, ok := s.Enums[name]; return ok }

	// values_ref, on fields and on their parts. "@messages" is the one special
	// form: it means the value set is the message catalog's keys, so it needs a
	// catalog to mean anything.
	checkValuesRef := func(where, ref string) {
		switch {
		case ref == "":
		case ref == "@messages":
			if len(s.Messages.Catalog) == 0 {
				errs = append(errs, fmt.Sprintf("%s uses values_ref \"@messages\" but the message catalog is empty", where))
			}
		case strings.HasPrefix(ref, "@"):
			errs = append(errs, fmt.Sprintf("%s uses values_ref %q; the only special value set is \"@messages\"", where, ref))
		case !enumExists(ref):
			errs = append(errs, fmt.Sprintf("%s names the value set %q, which is not declared under enums (%s)", where, ref, knownEnums(s.Enums)))
		}
	}

	for _, de := range sortedFieldIDs(s.Fields) {
		f := s.Fields[de]
		checkValuesRef(fmt.Sprintf("field %d", de), f.ValuesRef)
		for i, part := range f.Subfields.Parts {
			label := fmt.Sprintf("field %d part %d", de, i+1)
			if part.Tag != "" {
				label = fmt.Sprintf("field %d tag %s", de, part.Tag)
			}
			checkValuesRef(label, part.ValuesRef)
		}

		// Masking may be added to a field, never removed. A classification says
		// what the data is; turning the mask off does not change that, and a spec
		// that reads as if it did is a spec someone will trust.
		if f.LogMask != nil && !*f.LogMask && isSensitive(f.Sensitivity) {
			errs = append(errs, fmt.Sprintf(
				"field %d is classified %q and sets log_mask: false; masking follows the classification and cannot be switched off",
				de, f.Sensitivity))
		}

		// An amount is only comparable if its currency is real and is a currency.
		if cf := f.Format.CurrencyField; cf != nil {
			switch {
			case !fieldExists(*cf):
				errs = append(errs, fmt.Sprintf("field %d binds its currency to field %d, which is not declared", de, *cf))
			case s.Fields[*cf].ValuesRef != currencyEnum:
				errs = append(errs, fmt.Sprintf(
					"field %d binds its currency to field %d, which does not carry currency codes (its values_ref is %q, expected %q)",
					de, *cf, s.Fields[*cf].ValuesRef, currencyEnum))
			}
		}

		for i, entry := range f.Messages {
			where := fmt.Sprintf("field %d, message rule %d", de, i+1)
			checkValuesRef(where, entry.ValuesRef)
			for _, code := range mtiCodes(entry.MTI) {
				if _, ok := s.Messages.Catalog[code]; !ok {
					errs = append(errs, fmt.Sprintf("%s names message %q, which is not in the catalog", where, code))
					continue
				}
				// `response_value` relates a value to the request's, so a request
				// has nothing to relate to. Presence is the other axis, and
				// `usage` already answers it there.
				if entry.ResponseValue != "" && s.Messages.Catalog[code].Flow == "request" {
					errs = append(errs, fmt.Sprintf(
						"%s sets response_value: %s on %s, which is a request; there is no prior value for it to relate to. Presence is `usage`.",
						where, entry.ResponseValue, code))
				}
			}
			if entry.When != "" {
				checkWhen(&errs, where, entry.When, fieldExists, subExists)
			}
		}
	}

	// A pairing that names nothing leaves the responder with no message to build.
	for _, code := range sortedKeys(s.Messages.Catalog) {
		m := s.Messages.Catalog[code]
		if m.PairsWith != "" {
			if _, ok := s.Messages.Catalog[m.PairsWith]; !ok {
				errs = append(errs, fmt.Sprintf("message %s pairs with %q, which is not in the catalog", code, m.PairsWith))
			}
		}
		for _, to := range m.Transitions {
			if _, ok := s.Messages.Catalog[to]; !ok {
				errs = append(errs, fmt.Sprintf("message %s declares a transition to %q, which is not in the catalog", code, to))
			}
		}
	}

	for i, c := range s.Checks {
		where := fmt.Sprintf("check %q", c.Name)
		if c.Name == "" {
			where = fmt.Sprintf("check %d", i+1)
		}
		for _, code := range c.MTI {
			if _, ok := s.Messages.Catalog[code]; !ok {
				errs = append(errs, fmt.Sprintf("%s names message %q, which is not in the catalog", where, code))
			}
		}
		// A severity nobody recognises would silently become the default, and
		// the default is the strict one: a typo would start rejecting traffic.
		if c.Severity != "" && c.Severity != SeverityReject && c.Severity != SeverityWarn {
			errs = append(errs, fmt.Sprintf(
				"%s has severity %q; the levels are %q and %q", where, c.Severity, SeverityReject, SeverityWarn))
		}
		checkWhen(&errs, where, c.Assert, fieldExists, subExists)
	}

	// A telemetry dimension must be bounded. An unbounded one turns every
	// distinct value into a time series, which is how a metrics backend falls
	// over — and the spec is where that is knowable.
	for _, dim := range s.Observability.Dimensions {
		if dim == "mti" {
			continue
		}
		de, subs, ok := resolveDimension(dim, s)
		if !ok {
			errs = append(errs, fmt.Sprintf("observability dimension %q names no field, alias or subfield in this spec", dim))
			continue
		}
		if ok, why := dimensionIsBounded(s, de, subs); !ok {
			errs = append(errs, fmt.Sprintf(
				"observability dimension %q is unbounded: %s. Close the value set, give its values a `category`, or drop the dimension", dim, why))
		}
	}

	// A histogram names its subject and the axis it is grouped by, both as
	// aliases. Neither was checked: a typo produced no histogram and no error,
	// and the absence of a chart is not something anyone notices.
	for i, h := range s.Observability.Histograms {
		where := fmt.Sprintf("observability histogram %d", i+1)
		if h.Field != "" {
			where = fmt.Sprintf("observability histogram over %q", h.Field)
		}
		subject, _, ok := resolveDimension(h.Field, s)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s names no field or alias in this spec", where))
			continue
		}
		if h.By == "" {
			continue
		}
		by, _, ok := resolveDimension(h.By, s)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s groups by %q, which names no field or alias in this spec", where, h.By))
			continue
		}
		// Grouping an amount by something that is not its currency sums figures
		// that were never comparable, and the total looks perfectly plausible.
		if s.Fields[subject].Format.Kind == "amount" {
			if cf := s.Fields[subject].Format.CurrencyField; cf != nil && *cf != by {
				errs = append(errs, fmt.Sprintf(
					"%s groups by field %d, but that amount declares its currency as field %d; grouping by anything else adds up figures in different currencies",
					where, by, *cf))
			} else if cf == nil && s.Fields[by].ValuesRef != currencyEnum {
				errs = append(errs, fmt.Sprintf(
					"%s groups by field %d, which does not carry currency codes", where, by))
			}
		}
	}

	// A generator that draws from a value set names one, and a name that resolves
	// to nothing generates nothing — which shows up as a suite that passes over
	// traffic it never produced.
	checkDraw := func(where string, v simValue) {
		if v.Choose.From == "" {
			return
		}
		set, ok := s.Enums[v.Choose.From]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s draws from the value set %q, which is not declared under enums (%s)",
				where, v.Choose.From, knownEnums(s.Enums)))
			return
		}
		for code := range v.Choose.Weights {
			if _, ok := set.Values[code]; !ok {
				errs = append(errs, fmt.Sprintf("%s weights the value %q, which %q does not contain",
					where, code, v.Choose.From))
			}
		}
	}
	for _, de := range sortedInts(keysOfSim(s.Simulation.Defaults)) {
		checkDraw(fmt.Sprintf("simulation default for field %d", de), s.Simulation.Defaults[de])
	}
	for _, mti := range sortedKeysOfTemplates(s.Simulation.Templates) {
		for _, de := range sortedInts(keysOfSim(s.Simulation.Templates[mti])) {
			checkDraw(fmt.Sprintf("simulation template %s, field %d", mti, de), s.Simulation.Templates[mti][de])
		}
	}
	for i, m := range s.Simulation.Mix {
		if m.Use == "" {
			continue
		}
		if _, ok := s.Messages.Catalog[m.Use]; !ok {
			errs = append(errs, fmt.Sprintf("simulation mix entry %d generates %q, which is not in the catalog", i+1, m.Use))
		}
	}

	if err := checkWhenCycles(s, &errs); err != nil {
		errs = append(errs, err.Error())
	}
	return errs.err()
}

// checkWhen parses one expression and checks that every location it reads is
// declared. A condition over a field the spec never defined can only ever be
// false, silently.
func checkWhen(errs *resolveErrors, where, src string, fieldExists func(int) bool, subExists func(int, []string) bool) {
	if src == "" {
		return
	}
	e, err := ParseWhen(src)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s has a `when` that does not parse: %v", where, err))
		return
	}
	for _, ref := range e.Refs {
		if !fieldExists(ref.DE) {
			*errs = append(*errs, fmt.Sprintf("%s reads field %d, which this spec does not declare", where, ref.DE))
			continue
		}
		if len(ref.Subs) > 0 && !subExists(ref.DE, ref.Subs) {
			*errs = append(*errs, fmt.Sprintf("%s reads %s, and field %d declares no such part", where, ref, ref.DE))
		}
	}
}

// checkWhenCycles rejects a spec whose conditions depend on one another in a
// loop. Generation resolves conditions to a fixpoint, and a cycle is a fixpoint
// that never arrives.
func checkWhenCycles(s *Spec, errs *resolveErrors) error {
	// A field's presence depends on the fields its conditions read.
	deps := map[int]map[int]bool{}
	for de, f := range s.Fields {
		for _, entry := range f.Messages {
			if entry.When == "" {
				continue
			}
			e, err := ParseWhen(entry.When)
			if err != nil {
				continue // already reported
			}
			for _, ref := range e.Refs {
				if ref.DE == de {
					continue // a field conditioned on itself is caught below
				}
				if deps[de] == nil {
					deps[de] = map[int]bool{}
				}
				deps[de][ref.DE] = true
			}
		}
	}
	var path []int
	state := map[int]int{} // 0 unvisited, 1 on the current path, 2 done
	var walk func(int) []int
	walk = func(de int) []int {
		state[de] = 1
		path = append(path, de)
		for _, next := range sortedInts(deps[de]) {
			switch state[next] {
			case 1:
				// Report the cycle from where it closes.
				for i, v := range path {
					if v == next {
						return append(append([]int{}, path[i:]...), next)
					}
				}
			case 0:
				if cyc := walk(next); cyc != nil {
					return cyc
				}
			}
		}
		path = path[:len(path)-1]
		state[de] = 2
		return nil
	}
	for _, de := range sortedInts(keysOf(deps)) {
		if state[de] != 0 {
			continue
		}
		path = nil
		if cyc := walk(de); cyc != nil {
			parts := make([]string, 0, len(cyc))
			for _, v := range cyc {
				parts = append(parts, fmt.Sprint(v))
			}
			return fmt.Errorf("the `when` conditions of fields %s depend on one another in a cycle, so generation would never settle",
				strings.Join(parts, " -> "))
		}
	}
	return nil
}

// resolveDimension turns a telemetry dimension into the field it names. A
// dimension may be written as an alias (`resp_code`), a field number, or a
// dotted path into a composite (`3.1`).
func resolveDimension(dim string, s *Spec) (int, []string, bool) {
	if ref, err := parsePath(dim, 0); err == nil {
		if _, ok := s.Fields[ref.DE]; ok {
			return ref.DE, ref.Subs, true
		}
		return 0, nil, false
	}
	// Not a path: an alias, on the field or on one of its parts.
	for de, f := range s.Fields {
		if f.Alias == dim {
			return de, nil, true
		}
	}
	return 0, nil, false
}

// dimensionIsBounded reports whether a dimension can only take values from a
// declared set. An unbounded dimension turns every distinct value into its own
// time series, which is how a metrics backend falls over — and a spec is where
// that is knowable before it happens.
func dimensionIsBounded(s *Spec, de int, subs []string) (bool, string) {
	set := dimensionValueSet(s, de, subs)
	switch {
	case set == nil:
		return false, "it has no value set at all, so every distinct value becomes its own time series"
	case set.Closed:
		return true, ""
	case set.categorised():
		// An open set with categories still bounds the cardinality: a value nobody
		// listed lands in a bucket that was.
		return true, ""
	default:
		return false, "its value set is open and its values carry no `category`, so a value nobody listed has no bucket to land in"
	}
}

// dimensionValueSet resolves the value domain behind a dimension, following a
// values_ref into the catalog or reading the inline set.
func dimensionValueSet(s *Spec, de int, subs []string) *ValueSet {
	f, ok := s.Fields[de]
	if !ok {
		return nil
	}
	ref, inline := f.ValuesRef, f.ValidValues
	if len(subs) > 0 {
		ref, inline = "", nil
		for i, p := range f.Subfields.Parts {
			if p.Tag == subs[0] || (p.Tag == "" && fmt.Sprint(i+1) == subs[0]) {
				ref, inline = p.ValuesRef, p.ValidValues
				break
			}
		}
	}
	if inline != nil {
		return inline
	}
	if ref == "" || ref == "@messages" {
		// The message catalog is closed by construction: an MTI outside it is not
		// a message this spec knows.
		if ref == "@messages" {
			return &ValueSet{Closed: true, Values: nil}
		}
		return nil
	}
	return s.Enums[ref]
}

func sortedFieldIDs(m map[int]Field) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedKeys(m map[string]Message) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func knownEnums(m map[string]*ValueSet) string {
	if len(m) == 0 {
		return "none are declared"
	}
	return "declared: " + strings.Join(sortedKeysAny(m), ", ")
}

func mtiCodes(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func sortedInts(m any) []int {
	var out []int
	switch t := m.(type) {
	case map[int]bool:
		for k := range t {
			out = append(out, k)
		}
	case []int:
		out = append(out, t...)
	}
	sort.Ints(out)
	return out
}

func keysOf(m map[int]map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedKeysAny(m map[string]*ValueSet) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOfSim(m map[int]simValue) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedKeysOfTemplates(m map[string]map[int]simValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
