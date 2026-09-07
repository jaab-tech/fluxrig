// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package protodoc renders a protocol reference from a spec.
//
// The spec is field-major, because a field's rules are what change together: a
// scheme bulletin names one data element across several messages. A reader wants
// the opposite — "what does an 0200 carry" — so the per-message view is the
// matrix inverted, derived on every render rather than maintained.
//
// What the reference says is decided once, in Build, and drawn by a renderer per
// format. Two renderers that each read the spec would each decide, and would
// disagree the moment one of them changed.
package protodoc

import (
	"fmt"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// Scope selects how much of a spec is rendered.
type Scope string

const (
	// ScopePublic omits fields marked `scope: private`. Only this variant is
	// eligible for publication: a private field is a proprietary extension, and
	// the spec is where that is declared rather than remembered.
	ScopePublic Scope = "public"
	// ScopeComplete renders everything, for internal reference.
	ScopeComplete Scope = "complete"
)

// Options controls a render.
type Options struct {
	Scope Scope
	// Title overrides the heading. Empty uses the spec's own name.
	Title string
	// Source is the spec document as written. Given one, every section carries
	// the text it was derived from, lifted from the file rather than
	// re-serialised: a regenerated fragment loses the comments and quietly
	// differs from what is on disk, and anyone checking where a rule came from
	// is checking the disk.
	Source []byte
	// BaseDir is where a file wire source resolves against, so the wire layer
	// can be shown as it ends up rather than as the delta the spec wrote.
	BaseDir string
}

// Markdown renders the reference as Markdown.
func Markdown(spec *sdl.Spec, opts Options) string {
	return MarkdownDoc(Build(spec, opts))
}

// MarkdownDoc renders a document that has already been built.
func MarkdownDoc(doc *Document) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# %s\n\n", doc.Title)
	w("%d data elements", doc.Elements)
	if doc.Messages > 0 {
		w(", %d messages", doc.Messages)
	}
	w(".")
	if doc.Withheld > 0 {
		if doc.Withheld == 1 {
			w(" 1 private element is omitted from this variant.")
		} else {
			w(" %d private elements are omitted from this variant.", doc.Withheld)
		}
	}
	w("\n\n")
	// A reference outlives the deployment it described. The HTML has carried the
	// version since it existed; markdown is the variant that goes into a
	// repository or a docs site, where "which contract is this" is asked more
	// often, not less.
	if doc.Version != "" {
		w("Version %s\n\n", doc.Version)
	}

	if doc.Overview != "" {
		w("## Introduction\n\n%s\n", doc.Overview)
	}

	w("## How to read this\n\n")
	w("| Usage | Meaning |\n| :--- | :--- |\n")
	w("| mandatory | The element must be present. |\n")
	w("| optional | It may be present. |\n")
	w("| forbidden | It must not be present. |\n")
	w("| conditional | Which of the above applies depends on the message's own contents. |\n")
	w("\nA rule may carry a condition, shown under **When**: it applies only where the ")
	w("condition holds, and the first matching rule wins.\n\n")

	if doc.UsesValue {
		w("| `response_value` | Meaning |\n| :--- | :--- |\n")
		w("| echo | Carries the request's value; a difference is a fault. |\n")
		w("| new | Originated by the responder. |\n")
		w("| modified | Derived from the request's value and may legitimately differ. |\n\n")
	}

	if len(doc.MessageViews) > 0 {
		w("## Messages\n\n")
		for _, m := range doc.MessageViews {
			w("### %s — %s\n\n", m.MTI, orDash(m.Name))
			if m.Description != "" {
				w("%s\n\n", m.Description)
			}
			if m.Pairing != "" {
				w("%s\n\n", boldMTIs(m.Pairing))
			}
			if len(m.Rows) == 0 {
				w("_No element rules are declared for this message._\n\n")
				continue
			}
			cols := []string{"Element", "Name", "Usage"}
			if m.ShowResponse {
				cols = append(cols, "Response value")
			}
			if m.ShowWhen {
				cols = append(cols, "When")
			}
			// The element number reads right-aligned; everything after it is text.
			// The cell has to be closed: without the pipe the separator carries one
			// cell fewer than the header, and a table whose separator does not match
			// its header is not a table at all -- most renderers show the pipes.
			w("| %s |\n| ---: |%s\n", strings.Join(cols, " | "), strings.Repeat(" :--- |", len(cols)-1))
			for _, row := range m.Rows {
				w("| %d | %s | %s |", row.DE, orDash(row.Name), orDash(row.Usage))
				if m.ShowResponse {
					w(" %s |", orDash(row.ResponseValue))
				}
				if m.ShowWhen {
					w(" %s |", foldedRules(row))
				}
				w("\n")
			}
			w("\n")
		}
	}

	if len(doc.References) > 0 {
		w("## References\n\n")
		w("The documents this spec was written from. They are cited, not carried: ")
		w("a standards body sells its own, and a scheme issues its own under its terms.\n\n")
		for _, r := range doc.References {
			w("- **%s**", r.Title)
			if r.Publisher != "" {
				w(" — %s", r.Publisher)
			}
			w("\n")
			if r.Note != "" {
				w("  %s\n", r.Note)
			}
			if r.URL != "" {
				w("  <%s>\n", r.URL)
			}
		}
		w("\n")
	}

	w("## Data elements\n\n")
	for _, f := range doc.FieldViews {
		w("### DE %d — %s\n\n", f.DE, orDash(f.Name))
		if f.Description != "" {
			w("%s\n\n", f.Description)
		}
		// A note is set apart because it is a different kind of statement: the
		// description defines the element, the note says what will bite you about
		// it, and running them together buries the second.
		if f.Note != "" {
			w("> %s\n\n", f.Note)
		}
		for _, kv := range f.Facts {
			value := kv[1]
			if kv[0] == "Alias" {
				value = "`" + value + "`"
			}
			w("- **%s**: %s\n", kv[0], value)
		}
		if len(f.Facts) > 0 {
			w("\n")
		}

		if len(f.Rules) > 0 {
			cols := []string{"In", "Usage"}
			if f.ShowResp {
				cols = append(cols, "Response value")
			}
			if f.ShowValues {
				cols = append(cols, "Values")
			}
			if f.ShowWhen {
				cols = append(cols, "When")
			}
			if f.ShowNote {
				cols = append(cols, "Note")
			}
			w("| %s |\n|%s\n", strings.Join(cols, " | "), strings.Repeat(" :--- |", len(cols)))
			for _, rule := range f.Rules {
				w("| %s | %s |", orDash(rule.MTIs), orDash(rule.Usage))
				if f.ShowResp {
					w(" %s |", orDash(rule.ResponseValue))
				}
				if f.ShowValues {
					w(" %s |", codeOrDash(rule.Values))
				}
				if f.ShowWhen {
					w(" %s |", codeOrDash(rule.When))
				}
				if f.ShowNote {
					w(" %s |", orDash(rule.Note))
				}
				w("\n")
			}
			w("\n")
		}

		if p := f.Parts; p != nil {
			w("**Parts** (%s)\n\n", p.Layout)
			key := "#"
			align := "---:"
			if p.TLV {
				key, align = "Tag", ":---"
			}
			w("| %s | Name | Values |\n| %s | :--- | :--- |\n", key, align)
			for _, row := range p.Rows {
				k := row.Key
				if p.TLV {
					k = "`" + k + "`"
				}
				w("| %s | %s | %s |\n", k, orDash(row.Name), codeOrDash(row.Values))
			}
			w("\n")
		}

		for _, t := range f.ValueTables {
			if t.Closed {
				w("**%s** — this list is complete; anything else is invalid.\n\n", t.Heading)
			} else {
				w("**%s** — the documented set. Others may occur.\n\n", t.Heading)
			}
			cols := []string{"Value", "Name"}
			if t.HasCat {
				cols = append(cols, "Group")
			}
			if t.HasDesc {
				cols = append(cols, "Description")
			}
			w("| %s |\n|%s\n", strings.Join(cols, " | "), strings.Repeat(" :--- |", len(cols)))
			for _, row := range t.Rows {
				w("| `%s` | %s |", row.Code, orDash(row.Name))
				if t.HasCat {
					w(" %s |", orDash(row.Category))
				}
				if t.HasDesc {
					w(" %s |", orDash(row.Description))
				}
				w("\n")
			}
			w("\n")
		}
	}
	return b.String()
}

// foldedRules spells out a conditional element: which usage applies when.
func foldedRules(row MessageRow) string {
	if len(row.Rules) == 0 {
		return codeOrDash(row.When)
	}
	parts := make([]string, 0, len(row.Rules))
	for _, rule := range row.Rules {
		if rule.Condition == "" {
			parts = append(parts, rule.Usage+" otherwise")
			continue
		}
		parts = append(parts, rule.Usage+" when "+codeOrDash(rule.Condition))
	}
	return strings.Join(parts, "<br>")
}

func boldMTIs(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i+4 <= len(s) && isFourDigits(s[i:i+4]) {
			b.WriteString("**" + s[i:i+4] + "**")
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isFourDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) == 4
}

// cell prepares a value for a table cell. A pipe inside one ends the column
// wherever it appears — backticks do not protect it — and a `when` expression
// carries `||` for nearly every disjunction, so an unescaped condition silently
// shears its own row in half.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", `\|`)
}

func orDash(s string) string { return cell(s) }

func codeOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return "`" + cell(s) + "`"
}
