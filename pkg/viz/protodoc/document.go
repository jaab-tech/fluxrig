// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// A Document is what the reference says, decided once and rendered more than
// once.
//
// Every question of content lives here: which fields a scope admits, how the
// field-major matrix inverts into a per-message view, which columns carry
// anything, how several conditional rules fold into one row. A format decides
// only how to draw it. Two renderers that each read the spec would each answer
// those questions, and answer them differently the moment one of them is
// changed and the other is not.
type Document struct {
	Title    string
	SpecName string
	// Version is the spec's own. A printed reference with no version on it is a
	// hazard: paper outlives the deployment it described, and the reader has no
	// way to tell which protocol they are holding.
	Version   string
	Scope     Scope
	Elements  int // rendered, after the scope filter
	Messages  int
	Withheld  int  // omitted by the scope filter, and said out loud
	UsesValue bool // any rendered rule carries a response_value

	MessageViews []MessageView
	FieldViews   []FieldView
	// ValueSets are the declared sets, each documented once. A name mentioned in
	// a part's row or a rule's cell has to lead somewhere: without this it is a
	// word the reader is left to match against a table rendered under some other
	// element, if it was rendered at all.
	ValueSets []ValueTable

	// Overview is the protocol's own primer, as the spec wrote it. A reference
	// explains the vocabulary it uses; it cannot explain the protocol, and a
	// reader sent to find a standards document for that has been sent to buy one.
	Overview string
	// References are the documents the spec was written from, cited rather than
	// carried.
	References []Reference

	// Wire is the byte layout as it ends up, whole: the named base with this
	// spec's overrides applied. WireSource names where the base came from.
	Wire       string
	WireSource string

	// Source is the whole spec as written, when one was given. A reference is a
	// derived view, and a reader who doubts it should not have to go looking for
	// the file it was derived from.
	Source string
}

// Reference is one document the spec was written from.
type Reference struct {
	Title, Publisher, Note, URL string
}

// MessageView is one message and what it carries: the matrix, inverted.
type MessageView struct {
	MTI          string
	Name         string
	Description  string
	Pairing      string // prose: "A request. Its response is 0110."
	Rows         []MessageRow
	ShowResponse bool
	ShowWhen     bool
	Source       string
	SourceLine   int
}

// MessageRow is one data element in one message. Never one rule: an element
// with three conditional rules appearing three times reads as three
// contradictory answers, and the condition reconciling them is a column away.
type MessageRow struct {
	DE            int
	Name          string
	Usage         string
	ResponseValue string
	Rules         []RuleLine // empty when a single unconditional rule applies
	When          string     // set instead when exactly one condition applies
}

// RuleLine is one folded rule: "mandatory when field(22) == '05'".
type RuleLine struct {
	Usage     string
	Condition string // empty means this is the default
}

// FieldView is one data element and everything said about it.
type FieldView struct {
	DE          int
	Name        string
	Description string
	Note        string
	Facts       [][2]string
	Rules       []FieldRuleRow
	ShowResp    bool
	ShowValues  bool
	ShowWhen    bool
	ShowNote    bool
	Parts       *PartsView
	ValueTables []ValueTable
	Source      string
	SourceLine  int
	// Wire is the element's wire layer as it ends up: the named base and the
	// spec's overrides, resolved. What the spec wrote is often only the delta,
	// which says almost nothing about how the bytes are read.
	Wire string
	// WireSummary is the byte layout in one line — type, length, encoding,
	// prefix. A reader asking "what is this on the wire" should not have to open
	// a panel to be told it is a String of twelve ASCII characters.
	WireSummary [][2]string
}

// FieldRuleRow is one rule as the field's own section shows it, where the
// per-rule detail belongs.
type FieldRuleRow struct {
	MTIs          string
	Usage         string
	ResponseValue string
	Values        string
	When          string
	Note          string
}

// PartsView is a composite's parts, described from the semantic side.
type PartsView struct {
	Layout string
	TLV    bool
	Rows   []PartRow
}

// PartRow is one part of a composite.
type PartRow struct {
	Key    string // the tag, or the ordinal
	Name   string
	Values string
}

// ValueTable is one value domain, and the claim it makes about values outside
// it.
type ValueTable struct {
	Heading    string
	Closed     bool
	HasCat     bool
	HasDesc    bool
	Rows       []ValueRow
	Source     string
	SourceLine int
	// Name is the declared set's own name, when it has one. An inline set has
	// none, and belongs only to the element that wrote it.
	Name string
	// UsedBy are the elements this set governs.
	UsedBy []int
}

// ValueRow is one code and what it means.
type ValueRow struct {
	Code, Name, Category, Description string
}

// Build reads a spec and decides everything the reference will say.
func Build(spec *sdl.Spec, opts Options) *Document {
	if opts.Scope == "" {
		opts.Scope = ScopePublic
	}
	b := &builder{spec: spec, scope: opts.Scope}
	if len(opts.Source) > 0 {
		if wf, err := sdl.WireFields(opts.Source, opts.BaseDir); err == nil {
			b.wire = wf
		}
		// An element's label comes from the wire layer, where Moov already
		// declares one for every field a named base carries. The semantic
		// `name` overrides it and exists for the cases where the upstream
		// wording is wrong for this protocol.
		if wl, err := sdl.WireLabels(opts.Source, opts.BaseDir); err == nil {
			b.labels = wl
		}
		// A spec that will not index is still a spec worth documenting: the
		// fragments are an aid, not the reference.
		if idx, err := sdl.IndexSource(opts.Source); err == nil {
			b.src = idx
		}
	}

	doc := &Document{
		Title:    opts.Title,
		SpecName: spec.Name,
		Version:  spec.Version,
		Scope:    opts.Scope,
		Messages: len(spec.Messages.Catalog),
	}
	if doc.Title == "" {
		doc.Title = spec.Name
	}
	if doc.Title == "" {
		doc.Title = "Protocol reference"
	}

	// Counted from the same set the reference documents, or the heading and the
	// body disagree about how many elements the protocol has.
	counted := make(map[int]bool, len(spec.Fields)+len(b.labels))
	count := func(de int) {
		if counted[de] {
			return
		}
		counted[de] = true
		if b.visible(de) {
			doc.Elements++
		} else {
			doc.Withheld++
		}
	}
	for de := range spec.Fields {
		count(de)
	}
	for de := range b.labels {
		count(de)
	}
	if b.src != nil {
		doc.Source = b.src.Whole
	}
	doc.WireSource = spec.Wire.Source
	if len(opts.Source) > 0 {
		if wire, err := sdl.WireDocument(opts.Source, opts.BaseDir); err == nil {
			doc.Wire = strings.TrimRight(string(wire), "\n")
		}
	}
	doc.Overview = spec.Overview
	for _, r := range spec.References {
		doc.References = append(doc.References, Reference{
			Title: r.Title, Publisher: r.Publisher, Note: r.Note, URL: r.URL,
		})
	}
	doc.UsesValue = b.usesResponseValue()
	doc.ValueSets = b.valueSets()
	doc.MessageViews = b.messages()
	doc.FieldViews = b.fields()
	return doc
}

type builder struct {
	spec   *sdl.Spec
	scope  Scope
	src    *sdl.SourceIndex
	wire   map[int]string
	labels map[int]string
}

// label is what an element is called. The wire layer declares one for every
// field a named base carries, and the semantic `name` overrides it: a spec
// restating a label it already has is the same string in two places, and the two
// drift apart the first time one of them is corrected.
func (b *builder) label(de int, f sdl.Field) string {
	if f.Name != "" {
		return f.Name
	}
	return b.labels[de]
}

func (b *builder) fieldSource(de int) (string, int) {
	if b.src == nil {
		return "", 0
	}
	f := b.src.Fields[de]
	return f.Text, f.Line
}

func (b *builder) messageSource(mti string) (string, int) {
	if b.src == nil {
		return "", 0
	}
	f := b.src.Messages[mti]
	return f.Text, f.Line
}

func (b *builder) enumSource(ref string) (string, int) {
	if b.src == nil || ref == "" || ref == "@messages" {
		return "", 0
	}
	f := b.src.Enums[ref]
	return f.Text, f.Line
}

func (b *builder) visible(de int) bool {
	return b.scope == ScopeComplete || b.spec.Fields[de].IsPublic()
}

func (b *builder) usesResponseValue() bool {
	for de, f := range b.spec.Fields {
		if !b.visible(de) {
			continue
		}
		for _, rule := range f.Messages {
			if rule.ResponseValue != "" {
				return true
			}
		}
	}
	return false
}

func (b *builder) messages() []MessageView {
	if len(b.spec.Messages.Catalog) == 0 {
		return nil
	}
	byMessage := map[string]map[int][]sdl.MessageRule{}
	for de, f := range b.spec.Fields {
		if !b.visible(de) {
			continue
		}
		for _, rule := range f.Messages {
			for _, mti := range rule.Codes() {
				if byMessage[mti] == nil {
					byMessage[mti] = map[int][]sdl.MessageRule{}
				}
				byMessage[mti][de] = append(byMessage[mti][de], rule)
			}
		}
	}

	out := make([]MessageView, 0, len(b.spec.Messages.Catalog))
	for _, mti := range sortedMessages(b.spec.Messages.Catalog) {
		m := b.spec.Messages.Catalog[mti]
		text, line := b.messageSource(mti)
		v := MessageView{MTI: mti, Name: m.Name, Description: m.Meaning, Pairing: pairing(m),
			Source: text, SourceLine: line}

		des := make([]int, 0, len(byMessage[mti]))
		for de := range byMessage[mti] {
			des = append(des, de)
		}
		sort.Ints(des)

		for _, de := range des {
			rules := byMessage[mti][de]
			row := MessageRow{
				DE:            de,
				Name:          b.label(de, b.spec.Fields[de]),
				Usage:         usageOf(rules),
				ResponseValue: responseValueOf(rules),
			}
			if len(rules) == 1 {
				row.When = rules[0].When
			} else {
				for _, rule := range rules {
					row.Rules = append(row.Rules, RuleLine{Usage: rule.Usage, Condition: rule.When})
				}
			}
			if row.ResponseValue != "" {
				v.ShowResponse = true
			}
			if row.When != "" || len(row.Rules) > 0 {
				v.ShowWhen = true
			}
			v.Rows = append(v.Rows, row)
		}
		out = append(out, v)
	}
	return out
}

func (b *builder) fields() []FieldView {
	// Every element the protocol carries, not only the ones the semantic layer
	// annotates. The wire layer declares sixty-odd of them with their labels, and
	// a reference that showed only the annotated ones would be a reference with
	// holes in it -- which is what a spec used to avoid by carrying an entry
	// whose sole content was a label the wire layer already had.
	seen := make(map[int]bool, len(b.spec.Fields)+len(b.labels))
	ids := make([]int, 0, len(b.spec.Fields)+len(b.labels))
	add := func(de int) {
		if seen[de] || !b.visible(de) {
			return
		}
		seen[de] = true
		ids = append(ids, de)
	}
	for de := range b.spec.Fields {
		add(de)
	}
	for de := range b.labels {
		add(de)
	}
	sort.Ints(ids)

	out := make([]FieldView, 0, len(ids))
	for _, de := range ids {
		f := b.spec.Fields[de]
		text, line := b.fieldSource(de)
		v := FieldView{DE: de, Name: b.label(de, f), Description: f.Meaning, Note: f.Note,
			Source: text, SourceLine: line, Wire: b.wire[de]}

		if f.Alias != "" {
			v.Facts = append(v.Facts, [2]string{"Alias", f.Alias})
		}
		if f.Format.Kind != "" {
			kind := f.Format.Kind
			if f.Format.CurrencyField != nil {
				kind += fmt.Sprintf(", in the minor unit of DE %d", *f.Format.CurrencyField)
			}
			v.Facts = append(v.Facts, [2]string{"Format", kind})
		}
		if f.Sensitivity != "" && f.Sensitivity != "none" {
			v.Facts = append(v.Facts, [2]string{"Classification", f.Sensitivity})
		}
		if b.scope == ScopeComplete && !f.IsPublic() {
			v.Facts = append(v.Facts, [2]string{"Scope", "private — excluded from published documentation"})
		}

		for _, rule := range f.Messages {
			v.ShowResp = v.ShowResp || rule.ResponseValue != ""
			v.ShowWhen = v.ShowWhen || rule.When != ""
			v.ShowNote = v.ShowNote || rule.Note != ""
			v.ShowValues = v.ShowValues || rule.ValuesRef != "" || rule.ValidValues != nil
		}
		for _, rule := range f.Messages {
			v.Rules = append(v.Rules, FieldRuleRow{
				MTIs:          strings.Join(rule.Codes(), ", "),
				Usage:         rule.Usage,
				ResponseValue: rule.ResponseValue,
				Values:        b.valuesLabel(rule.ValuesRef, rule.ValidValues),
				When:          rule.When,
				Note:          rule.Note,
			})
		}

		if len(f.Subfields.Parts) > 0 {
			p := &PartsView{Layout: f.Subfields.Layout, TLV: f.Subfields.Layout == "tlv"}
			for i, part := range f.Subfields.Parts {
				key := fmt.Sprint(i + 1)
				if p.TLV {
					key = part.Tag
				}
				p.Rows = append(p.Rows, PartRow{Key: key, Name: part.Name, Values: b.valuesLabel(part.ValuesRef, part.ValidValues)})
			}
			v.Parts = p
		}

		v.WireSummary = wireSummary(v.Wire)
		v.ValueTables = b.valueTables(f)
		out = append(out, v)
	}
	return out
}

func (b *builder) valueTables(f sdl.Field) []ValueTable {
	var out []ValueTable
	if set := b.resolveSet(f.ValuesRef, f.ValidValues); set != nil {
		heading := "Values"
		for _, rule := range f.Messages {
			if rule.ValuesRef != "" || rule.ValidValues != nil {
				heading = "Values, where no message narrows them"
				break
			}
		}
		t := valueTable(heading, set)
		t.Source, t.SourceLine = b.enumSource(f.ValuesRef)
		out = append(out, t)
	}
	seen := map[string]bool{}
	for _, rule := range f.Messages {
		set := b.resolveSet(rule.ValuesRef, rule.ValidValues)
		if set == nil {
			continue
		}
		key := rule.ValuesRef + "|" + strings.Join(rule.Codes(), ",")
		if seen[key] {
			continue
		}
		seen[key] = true
		t := valueTable("Values in "+strings.Join(rule.Codes(), ", "), set)
		t.Source, t.SourceLine = b.enumSource(rule.ValuesRef)
		out = append(out, t)
	}
	return out
}

func valueTable(heading string, set *sdl.ValueSet) ValueTable {
	t := ValueTable{Heading: heading, Closed: set.Closed}
	for _, code := range sortedValues(set.Values) {
		v := set.Values[code]
		t.HasCat = t.HasCat || v.Category != ""
		t.HasDesc = t.HasDesc || v.Meaning != ""
		t.Rows = append(t.Rows, ValueRow{Code: code, Name: v.Name, Category: v.Category, Description: v.Meaning})
	}
	return t
}

func (b *builder) resolveSet(ref string, inline *sdl.ValueSet) *sdl.ValueSet {
	if inline != nil && len(inline.Values) > 0 {
		return inline
	}
	if ref == "" || ref == "@messages" {
		return nil
	}
	if set := b.spec.Enums[ref]; set != nil && len(set.Values) > 0 {
		return set
	}
	return nil
}

// valuesLabel names where a domain comes from without repeating it: the table
// itself is rendered once, under the set that owns it.
func (b *builder) valuesLabel(ref string, inline *sdl.ValueSet) string {
	switch {
	case inline != nil:
		return fmt.Sprintf("%d listed", len(inline.Values))
	case ref == "@messages":
		return "the message catalog"
	case ref != "":
		return ref
	default:
		return ""
	}
}

func pairing(m sdl.Message) string {
	switch {
	case m.Flow == "request" && m.PairsWith != "":
		return "A request. Its response is " + m.PairsWith + "."
	case m.Flow == "response" && m.PairsWith != "":
		return "A response. It answers " + m.PairsWith + "."
	case m.PairsWith != "":
		return "Paired with " + m.PairsWith + "."
	case m.Flow != "":
		return "A " + m.Flow + "."
	}
	return ""
}

// usageOf collapses an element's rules for one message into a single answer.
// Several usages mean the answer depends on the message's own contents, and
// saying so is more use than picking one of them to show.
func usageOf(rules []sdl.MessageRule) string {
	distinct := map[string]bool{}
	for _, rule := range rules {
		distinct[rule.Usage] = true
	}
	if len(distinct) == 1 {
		return rules[0].Usage
	}
	return "conditional"
}

// responseValueOf reports the provenance when every rule agrees. Rules that
// disagree are a per-case distinction, and the field's own section carries it.
func responseValueOf(rules []sdl.MessageRule) string {
	first := rules[0].ResponseValue
	for _, rule := range rules {
		if rule.ResponseValue != first {
			return "varies"
		}
	}
	return first
}

func sortedMessages(m map[string]sdl.Message) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedValues[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wireSummary reads the shape out of a resolved wire block. It reports only what
// the block actually says: a field with no declared encoding has none, and
// inventing a default here would be inventing one for the parser too.
func wireSummary(wire string) [][2]string {
	if wire == "" {
		return nil
	}
	var w struct {
		Type    string `yaml:"type"`
		Length  *int   `yaml:"length"`
		Enc     string `yaml:"enc"`
		Prefix  string `yaml:"prefix"`
		Padding struct {
			Type string `yaml:"type"`
			Pad  string `yaml:"pad"`
		} `yaml:"padding"`
	}
	if err := yaml.Unmarshal([]byte(wire), &w); err != nil {
		return nil
	}
	var out [][2]string
	if w.Type != "" {
		out = append(out, [2]string{"Wire type", w.Type})
	}
	if w.Length != nil {
		out = append(out, [2]string{"Length", fmt.Sprint(*w.Length)})
	}
	if w.Enc != "" {
		out = append(out, [2]string{"Encoding", w.Enc})
	}
	if w.Prefix != "" {
		out = append(out, [2]string{"Length prefix", w.Prefix})
	}
	if w.Padding.Type != "" && w.Padding.Type != "None" {
		pad := w.Padding.Type
		if w.Padding.Pad != "" {
			pad += " with " + w.Padding.Pad
		}
		out = append(out, [2]string{"Padding", pad})
	}
	return out
}

// valueSets documents each declared set once, in the order a reader meets them.
func (b *builder) valueSets() []ValueTable {
	names := make([]string, 0, len(b.spec.Enums))
	for name := range b.spec.Enums {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]ValueTable, 0, len(names))
	for _, name := range names {
		set := b.spec.Enums[name]
		if set == nil || len(set.Values) == 0 {
			continue
		}
		t := valueTable(name, set)
		t.Name = name
		t.Source, t.SourceLine = b.enumSource(name)
		t.UsedBy = b.usersOf(name)
		out = append(out, t)
	}
	return out
}

// usersOf lists the elements a set governs, so its entry says where it applies
// rather than leaving the reader to search for it.
func (b *builder) usersOf(name string) []int {
	var out []int
	for de, f := range b.spec.Fields {
		if !b.visible(de) {
			continue
		}
		used := f.ValuesRef == name
		for _, rule := range f.Messages {
			used = used || rule.ValuesRef == name
		}
		for _, part := range f.Subfields.Parts {
			used = used || part.ValuesRef == name
		}
		if used {
			out = append(out, de)
		}
	}
	sort.Ints(out)
	return out
}
