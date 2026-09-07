// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// HTML renders the reference as a self-contained page.
//
// Self-contained is the requirement, not a convenience: a protocol reference
// gets mailed, attached to a ticket, and opened from a laptop with no network.
// Nothing is fetched — no scripts, no stylesheets, no fonts — so the file that
// arrives is the file that renders.
//
// The page carries no branding. Someone documenting their own protocol should
// not find another company's mark on it; the only attribution is a line naming
// what generated the file.
func HTML(spec *sdl.Spec, opts Options) string {
	return HTMLDoc(Build(spec, opts))
}

// HTMLDoc renders a document that has already been built.
func HTMLDoc(doc *Document) string {
	var b strings.Builder
	// Fragments live out of the document's flow and are shown in one dialog.
	// Inline they were a box under every element, and sixty boxes a reader has
	// to scroll past are sixty boxes between them and the next element.
	var store strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	// Literal markup goes through p, never through w: a stray %% in generated
	// content passed as a format string would be read as a verb and corrupt the
	// output around it.
	lit := func(parts ...string) {
		for _, s := range parts {
			b.WriteString(s)
		}
	}

	link := newLinker(doc)
	lit("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	lit("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	w("<title>%s</title>\n<style>%s</style>\n</head>\n<body>\n", esc(doc.Title), pageCSS)

	// Home is wherever the reader lands: the first pane.
	home := "messages"
	if p := panes(doc); len(p) > 0 {
		home = p[0].id
	}
	lit("<header><div class=\"wrap hrow\">\n<div>\n")
	w("<div class=\"brand\"><a class=\"home\" id=\"home\" href=\"#pane-%s\" aria-label=\"Back to %s\">%s</a>"+
		"<span class=\"bar\"></span><span class=\"eyebrow\">Protocol reference · %s</span></div>\n",
		esc(home), esc(home), logoMark(""), esc(string(doc.Scope)))

	w("<h1>%s</h1>\n", esc(doc.Title))
	w("<p class=\"sub\">%d data elements", doc.Elements)
	if doc.Messages > 0 {
		w(", %d messages", doc.Messages)
	}
	lit(".")
	if doc.Withheld > 0 {
		if doc.Withheld == 1 {
			lit(" 1 private element is omitted from this variant.")
		} else {
			w(" %d private elements are omitted from this variant.", doc.Withheld)
		}
	}
	lit("</p>\n")
	// A reference outlives the deployment it described, and paper carries no
	// URL. Without the version on it, a reader cannot tell which protocol they
	// are holding.
	if doc.Version != "" {
		w("<p class=\"ver\">Version %s</p>\n", esc(doc.Version))
	}
	lit("</div>\n")
	lit("<div class=\"hbtns\">")
	w("<button id=\"theme\" class=\"iconbtn\" type=\"button\" aria-label=\"Switch theme\" title=\"Switch theme\">%s</button>", themeIcon)
	lit("</div>\n")
	lit("</div></header>\n")

	// Tabs, so a reference of sixty elements is not one page to scroll. Each is
	// a pane; printing shows them all, because a PDF has no tabs.
	lit(`<div class="tabbar"><div class="wrap tabrow">`)
	// The mark travels with the bar. Once the header has scrolled away it is the
	// only way back that is still on screen.
	w(`<a class="home tabhome" href="#pane-%s" aria-label="Back to %s">%s</a>`, esc(home), esc(home), logoMark("-bar"))
	lit(`<div class="tabs" role="tablist">`)
	for _, t := range panes(doc) {
		sel := "false"
		if t.first {
			sel = "true"
		}
		w(`<button class="tab" role="tab" data-pane="%s" aria-selected="%s">%s</button>`, t.id, sel, esc(t.label))
	}
	lit("</div></div></div>\n")

	// One index, and it belongs to the pane on screen. Tabs choose the pane;
	// this chooses the place within it. Anything else — a second row of tabs, a
	// row of chips — is a third way to move through a document that already has
	// two, and the reader has to learn which one reaches what.
	lit("<div class=\"wrap layout\">\n<nav aria-label=\"Contents\">\n")
	// A panel with no visible way out is a trap on a narrow screen: the scrim
	// closes it, but nothing says so.
	w("<button id=\"navclose\" class=\"iconbtn navclose\" type=\"button\" aria-label=\"Close contents\">%s</button>\n", closeIcon)
	grp := func(pane, label string) {
		w("<div class=\"grp\" data-pane=\"%s\">%s</div>\n", esc(pane), esc(label))
	}
	navTo := func(pane, href, k, v string, sub bool) {
		cls := ""
		if sub {
			cls = " class=\"sub\""
		}
		w("<a%s data-pane=\"%s\" href=\"#%s\"><span class=\"k\">%s</span><span class=\"v\">%s</span></a>\n",
			cls, esc(pane), esc(href), esc(k), esc(v))
	}
	// The groups follow the tabs, so the panel reads in the order the tabs do.
	if len(doc.MessageViews) > 0 {
		grp("messages", "Messages")
		for _, m := range doc.MessageViews {
			navTo("messages", "m-"+m.MTI, m.MTI, m.Name, false)
		}
	}
	grp("elements", "Data elements")
	for _, f := range doc.FieldViews {
		navTo("elements", fmt.Sprintf("de-%d", f.DE), strconv.Itoa(f.DE), f.Name, false)
	}
	grp("help", "Help")
	if doc.Overview != "" {
		navTo("help", "intro", "§", "Introduction", false)
	}
	navTo("help", "words", "§", "How to read this", false)
	navTo("help", "help", "§", "The words used here", false)
	// The eight groups of words are the reason this index exists: a reader
	// arrives at the glossary already looking for one of them.
	for _, g := range Glossary() {
		navTo("help", g.Anchor(), "", g.Name, true)
	}
	if len(doc.ValueSets) > 0 {
		navTo("help", "values", "§", "Value sets", false)
		for _, t := range doc.ValueSets {
			navTo("help", "values-"+t.Name, "", t.Name, true)
		}
	}
	if len(doc.References) > 0 {
		navTo("help", "refs", "§", "References", false)
	}
	if doc.Source != "" {
		grp("spec", "The spec")
		navTo("spec", "written", "§", "The spec, as written", false)
		if doc.Wire != "" {
			navTo("spec", "wire", "§", "The wire layer, resolved", false)
		}
	}
	lit("</nav>\n<main>\n")

	// A printed reference has no sidebar and no search. Without a contents page
	// a reader looking for DE 39 turns pages until they find it.
	if len(doc.MessageViews) > 0 || len(doc.FieldViews) > 0 {
		lit(`<section class="printonly toc"><h2>Contents</h2>`)
		if len(doc.MessageViews) > 0 {
			lit(`<p class="tocg">Messages</p><ul class="toclist">`)
			for _, m := range doc.MessageViews {
				w("<li><span class=\"k\">%s</span> %s</li>", esc(m.MTI), esc(m.Name))
			}
			lit("</ul>")
		}
		lit(`<p class="tocg">Data elements</p><ul class="toclist">`)
		for _, f := range doc.FieldViews {
			w("<li><span class=\"k\">%d</span> %s</li>", f.DE, esc(f.Name))
		}
		lit("</ul></section>\n")
	}

	lit(`<section class="pane" id="pane-messages" data-pane="messages">`)
	if len(doc.MessageViews) > 0 {
		lit("<h2>Messages</h2>\n")
		for _, m := range doc.MessageViews {
			w("<h3 id=\"m-%s\">%s — %s</h3>\n", esc(m.MTI), esc(m.MTI), esc(m.Name))
			if m.Description != "" {
				w("<p>%s</p>\n", esc(m.Description))
			}
			if m.Pairing != "" {
				w("<p class=\"pair\">%s</p>\n", link.prose(m.Pairing))
			}
			if len(m.Rows) == 0 {
				lit("<p class=\"nil\">No element rules are declared for this message.</p>\n")
				continue
			}
			lit("<div class=\"tw\"><table><thead><tr><th class=\"n\">Element</th><th>Name</th><th>Usage</th>")
			if m.ShowResponse {
				lit("<th>Response value</th>")
			}
			if m.ShowWhen {
				lit("<th>When</th>")
			}
			lit("</tr></thead><tbody>\n")
			for _, row := range m.Rows {
				// Both the number and the name lead to the element: the number is
				// what a reader scans, the name is what they reach for.
				w("<tr><td class=\"n\"><a class=\"xref\" href=\"#de-%d\">%d</a></td>"+
					"<td><a class=\"xref\" href=\"#de-%d\">%s</a></td><td>%s</td>",
					row.DE, row.DE, row.DE, esc(row.Name), chip("u", row.Usage))
				if m.ShowResponse {
					w("<td>%s</td>", chip("r", row.ResponseValue))
				}
				if m.ShowWhen {
					w("<td>%s</td>", foldedRulesHTML(row, link))
				}
				lit("</tr>\n")
			}
			lit("</tbody></table></div>\n")
			if m.Source != "" {
				lit(srcButton("msg-"+m.MTI, "yaml", m.Source))
				store.WriteString(panelFor("msg-"+m.MTI, m.MTI+" — the spec, as written",
					fmt.Sprintf("From line %d of the spec document.", m.SourceLine), m.Source, m.SourceLine))
			}
		}
	}
	lit("</section>\n")

	lit(`<section class="pane" id="pane-elements" data-pane="elements">`)
	lit("<h2>Data elements</h2>\n")
	for _, f := range doc.FieldViews {
		lit(`<div class="head">`)
		w("<h3 id=\"de-%d\">DE %d — %s</h3>", f.DE, f.DE, esc(f.Name))
		// The source belongs beside the title it explains, not at the foot of a
		// section the reader has already scrolled past.
		if f.Wire != "" || f.Source != "" {
			lit(`<div class="heads">`)
			lit(srcButton(fmt.Sprintf("wire-%d", f.DE), "wire", f.Wire))
			lit(srcButton(fmt.Sprintf("yaml-%d", f.DE), "yaml", f.Source))
			lit("</div>")
		}
		lit("</div>\n")
		if f.Description != "" {
			w("<p>%s</p>\n", esc(f.Description))
		}
		if f.Note != "" {
			w("<blockquote>%s</blockquote>\n", esc(f.Note))
		}
		// The byte layout, in a line. A reader asking what an element is on the
		// wire should not have to open a panel to be told.
		if len(f.WireSummary) > 0 {
			lit(`<ul class="facts wire">`, "\n")
			for _, kv := range f.WireSummary {
				value := esc(kv[1])
				if _, ok := define(kv[1]); ok {
					value = term(kv[1])
				}
				w("<li><strong>%s</strong> %s</li>\n", esc(kv[0]), value)
			}
			lit("</ul>\n")
		}
		if len(f.Facts) > 0 {
			lit("<ul class=\"facts\">\n")
			for _, kv := range f.Facts {
				value := esc(kv[1])
				switch kv[0] {
				case "Alias":
					value = "<code>" + value + "</code>"
				case "Classification":
					value = term(kv[1])
				case "Format":
					// The kind is the defined word; what follows it names the
					// element carrying the currency, which is a link.
					kind, rest, _ := strings.Cut(kv[1], ",")
					value = term(kind) + link.deRef(rest)
				}
				w("<li><strong>%s</strong> %s</li>\n", esc(kv[0]), value)
			}
			lit("</ul>\n")
		}

		if len(f.Rules) > 0 {
			lit("<div class=\"tw\"><table><thead><tr><th>In</th><th>Usage</th>")
			if f.ShowResp {
				lit("<th>Response value</th>")
			}
			if f.ShowValues {
				lit("<th>Values</th>")
			}
			if f.ShowWhen {
				lit("<th>When</th>")
			}
			if f.ShowNote {
				lit("<th>Note</th>")
			}
			lit("</tr></thead><tbody>\n")
			for _, rule := range f.Rules {
				w("<tr><td class=\"mono\">%s</td><td>%s</td>", link.mtiList(rule.MTIs), chip("u", rule.Usage))
				if f.ShowResp {
					w("<td>%s</td>", chip("r", rule.ResponseValue))
				}
				if f.ShowValues {
					w("<td>%s</td>", link.valueSetRef(rule.Values))
				}
				if f.ShowWhen {
					w("<td>%s</td>", link.condition(rule.When))
				}
				if f.ShowNote {
					w("<td>%s</td>", textCell(rule.Note))
				}
				lit("</tr>\n")
			}
			lit("</tbody></table></div>\n")
		}

		if p := f.Parts; p != nil {
			w("<p class=\"lbl\"><strong>Parts</strong> (%s)</p>\n", term(p.Layout))
			head := "#"
			if p.TLV {
				head = "Tag"
			}
			w("<div class=\"tw\"><table><thead><tr><th class=\"n\">%s</th><th>Name</th><th>Values</th></tr></thead><tbody>\n", head)
			for _, row := range p.Rows {
				w("<tr><td class=\"n\">%s</td><td>%s</td><td>%s</td></tr>\n",
					esc(row.Key), esc(row.Name), link.valueSetRef(row.Values))
			}
			lit("</tbody></table></div>\n")
		}

		for vi, t := range f.ValueTables {
			lit(valueTableHTML(t, true))
			if t.Source != "" {
				id := fmt.Sprintf("vals-%d-%d", f.DE, vi)
				lit(srcButton(id, "yaml", t.Source))
				store.WriteString(panelFor(id, t.Heading+" — the spec, as written",
					fmt.Sprintf("From line %d of the spec document.", t.SourceLine), t.Source, t.SourceLine))
			}
		}
		// The wire layer is assembled rather than written, so it belongs to no
		// line of any file and is not numbered as if it did.
		store.WriteString(panelFor(fmt.Sprintf("wire-%d", f.DE),
			fmt.Sprintf("DE %d — wire layer", f.DE),
			"As it ends up: the named base and this spec's overrides, resolved. It is assembled rather than written, so it has no line in any file.",
			f.Wire, 0))
		store.WriteString(panelFor(fmt.Sprintf("yaml-%d", f.DE),
			fmt.Sprintf("DE %d — the spec, as written", f.DE),
			fmt.Sprintf("From line %d of the spec document.", f.SourceLine),
			f.Source, f.SourceLine))
	}
	lit("</section>\n")

	lit(`<section class="pane" id="pane-help" data-pane="help">`)
	if doc.Overview != "" {
		lit(`<div class="sect" data-sec="intro">`)
		lit("<h2 id=\"intro\">Introduction</h2>\n")
		// It has a tab of its own now, so folding it would be the same gesture
		// asked for twice.
		lit(paragraphsHTML(doc.Overview))
		lit("</div>\n")
	}
	lit(`<div class="sect" data-sec="words">`)
	lit("<h2 id=\"words\">How to read this</h2>\n")
	lit(`<div class="tw"><table><thead><tr><th>Usage</th><th>Meaning</th></tr></thead><tbody>` +
		`<tr><td>` + chip("u", "mandatory") + `</td><td>The element must be present.</td></tr>` +
		`<tr><td>` + chip("u", "optional") + `</td><td>It may be present.</td></tr>` +
		`<tr><td>` + chip("u", "forbidden") + `</td><td>It must not be present.</td></tr>` +
		`<tr><td>` + chip("u", "conditional") + `</td><td>Which of the above applies depends on the message's own contents.</td></tr>` +
		"</tbody></table></div>\n")
	lit("<p>A rule may carry a condition, shown under <strong>When</strong>: it applies only " +
		"where the condition holds, and the first matching rule wins.</p>\n")
	if doc.UsesValue {
		lit(`<div class="tw"><table><thead><tr><th><code>response_value</code></th><th>Meaning</th></tr></thead><tbody>` +
			`<tr><td>` + chip("r", "echo") + `</td><td>Carries the request's value; a difference is a fault.</td></tr>` +
			`<tr><td>` + chip("r", "new") + `</td><td>Originated by the responder.</td></tr>` +
			`<tr><td>` + chip("r", "modified") + `</td><td>Derived from the request's value and may legitimately differ.</td></tr>` +
			"</tbody></table></div>\n")
	}

	lit("<h2 id=\"help\">Help</h2>\n")
	lit("<p>The words this reference uses, and what each one means. Every one of them ")
	lit("is a link from wherever it appears above.</p>\n")
	for _, g := range Glossary() {
		w("<h3 class=\"helpg\" id=\"%s\">%s</h3>\n<dl class=\"help\">\n", esc(g.Anchor()), esc(g.Name))
		for _, t := range g.Terms {
			w("<dt id=\"help-%s\"><code>%s</code></dt><dd>%s</dd>\n", esc(t.Key), esc(t.Key), esc(t.Long))
		}
		lit("</dl>\n")
	}
	lit("</div>\n")

	if len(doc.ValueSets) > 0 {
		lit(`<div class="sect" data-sec="values">`)
		lit("<h2 id=\"values\">Value sets</h2>\n")
		lit("<p>Every set this spec declares, and the elements it governs. ")
		lit("A set is documented here once; the elements that use it link back to this.</p>\n")
		for _, t := range doc.ValueSets {
			w("<h3 id=\"values-%s\"><code>%s</code></h3>\n", esc(t.Name), esc(t.Name))
			if len(t.UsedBy) > 0 {
				lit("<p class=\"pair\">Governs ")
				for i, de := range t.UsedBy {
					if i > 0 {
						lit(", ")
					}
					w(`<a class="xref" href="#de-%d">DE %d</a>`, de, de)
				}
				lit(".</p>\n")
			}
			lit(valueTableHTML(t, false))
		}
		lit("</div>\n")
	}

	if len(doc.References) > 0 {
		lit(`<div class="sect" data-sec="refs">`)
		lit("<h2 id=\"refs\">References</h2>\n")
		lit("<p>The documents this spec was written from. They are cited, not carried: " +
			"a standards body sells its own, and a scheme issues its own under its terms.</p>\n")
		lit(`<ul class="refs">`)
		for _, r := range doc.References {
			lit("<li><strong>", esc(r.Title), "</strong>")
			if r.Publisher != "" {
				lit(" — ", esc(r.Publisher))
			}
			if r.Note != "" {
				lit("<br><span class=\"muted\">", esc(r.Note), "</span>")
			}
			if r.URL != "" {
				lit("<br><span class=\"muted mono\">", esc(r.URL), "</span>")
			}
			lit("</li>")
		}
		lit("</ul>\n")
		lit("</div>\n")
	}
	lit("</section>\n")

	if doc.Source != "" {
		lit(`<section class="pane" id="pane-spec" data-pane="spec">`)
		lit("<h2>The spec</h2>\n")
		lit(`<div class="sect" data-sec="written">`)
		lit("<h3 id=\"written\">The spec, as written</h3>\n")
		lit("<p>Sections fold; the top two levels start open.</p>\n")
		w(`<div class="yaml">%s</div>`+"\n", renderYAML(doc.Source, 2, 1))
		lit("</div>\n")

		// The byte layout is not in the file when a base is named, so a page
		// claiming everything came from the document above would be claiming half.
		if doc.Wire != "" {
			lit(`<div class="sect" data-sec="wire">`)
			lit("<h3 id=\"wire\">The wire layer, resolved</h3>\n")
			// Where the layout came from decides what is true about it. A named
			// base is resolved from the linked library and cannot drift from
			// upstream; a path is a second file the reader can open; and with
			// neither, the layout is in the document above and nothing was merged.
			switch {
			case strings.HasPrefix(doc.WireSource, "moov:"):
				w("<p>Not part of the document above. <code>%s</code> names a base resolved from the "+
					"linked library, so it cannot drift from upstream and arrives with a dependency "+
					"update. This is that base with this spec's own overrides applied.</p>\n",
					esc(doc.WireSource))
			case doc.WireSource != "":
				w("<p>Not part of the document above. It is read from <code>%s</code>, a wire document "+
					"beside the spec, and shown here with this spec's own overrides applied.</p>\n",
					esc(doc.WireSource))
			default:
				lit("<p>Declared in the document above, under <code>wire.fields</code>: this spec names " +
					"no base, so nothing was merged into it. Shown here as the parser reads it.</p>\n")
			}
			w(`<div class="yaml nonum">%s</div>`+"\n", renderYAML(doc.Wire, 2, 0))
			lit("</div>\n")
		}
		lit("</section>\n")
	}

	lit("</main>\n</div>\n")
	lit(`<div class="scrim" hidden aria-hidden="true"></div>` + "\n")
	w(`<div class="float">`+
		`<button id="menu" type="button" aria-label="Contents" aria-expanded="false" title="Contents">%s</button>`+
		`<button id="top" type="button" aria-label="Back to top" title="Back to top">%s</button>`+
		"</div>\n", menuIcon, upIcon)
	lit("<footer><div class=\"wrap\"><p>Every table here is derived from one spec on each render, " +
		"never maintained alongside it. Generated by <code>fluxrig spec doc</code>.</p>")
	// On paper the browser prints the file's path, not this. A reference that
	// outlives its deployment has to say which protocol and which variant it is.
	w("<p class=\"printonly\">%s", esc(doc.SpecName))
	if doc.Version != "" {
		w(" · version %s", esc(doc.Version))
	}
	w(" · %s variant</p>\n", esc(string(doc.Scope)))
	lit("</div></footer>\n")
	// One dialog for every fragment: the content is copied in when a button asks
	// for it, so the document carries the panels without carrying them inline.
	w(`<dialog id="srcdlg"><div class="dlghead"><strong id="srcdlgtitle"></strong>`+
		`<button id="srcdlgclose" class="iconbtn" type="button" aria-label="Close">%s</button></div>`+
		`<div id="srcdlgbody"></div></dialog>`+"\n", closeIcon)
	// Every defined word has an entry the dialog can show, so meeting one in a
	// table does not mean leaving the table to find out what it means.
	for _, g := range Glossary() {
		for _, t := range g.Terms {
			store.WriteString(fmt.Sprintf(
				`<div class="srcpanel term-def" id="src-term-%s" data-title="%s" hidden>`+
					`<p class="grp">%s</p><p>%s</p>`+
					`<p class="cap"><a href="#help-%s">See it among the others</a></p></div>`+"\n",
				esc(t.Key), esc(t.Key), esc(g.Name), esc(t.Long), esc(t.Key)))
		}
	}
	w(`<div id="srcstore" hidden>%s</div>`+"\n", store.String())
	w("<script>%s</script>\n</body>\n</html>\n", pageJS)
	return b.String()
}

func esc(s string) string { return html.EscapeString(s) }

// paragraphsHTML renders authored prose. Blank lines separate paragraphs and
// nothing else is interpreted: a spec is written by someone else, and prose that
// becomes markup is prose that can rewrite the page around it.
func paragraphsHTML(text string) string {
	var b strings.Builder
	for _, p := range splitParagraphs(text) {
		b.WriteString("<p>" + esc(p) + "</p>\n")
	}
	return b.String()
}

// splitParagraphs breaks authored prose on blank lines and joins the wrapping
// within each: a spec is written to be edited in a text file, and its line
// breaks are the author's margin rather than their meaning.
func splitParagraphs(text string) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		if p := strings.Join(strings.Fields(para), " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// srcButton is the control beside a heading. Two of them sit there — the wire
// layer and the spec — so a reader asking "why does it say that" reaches for the
// answer at the title rather than at the end of the section.
func srcButton(id, label, body string) string {
	if body == "" {
		return ""
	}
	return fmt.Sprintf(`<button class="srcbtn" type="button" data-src="%s" aria-expanded="false">%s</button>`,
		esc(id), esc(label))
}

// panelFor is what a button opens. startLine is where the fragment sits in the
// document it was cut from; zero means it has no place in one, which is the case
// for a wire layer assembled from a base and a delta.
func panelFor(id, title, caption, body string, startLine int) string {
	if body == "" {
		return ""
	}
	// Both go through the same renderer: a wire block is YAML too, and rendering
	// it any other way is a second way for it to look wrong.
	rendered := `<div class="yaml inline nonum">` + renderYAML(body, 99, 0) + `</div>`
	if startLine > 0 {
		rendered = `<div class="yaml inline">` + renderYAML(body, 99, startLine) + `</div>`
	}
	return fmt.Sprintf(`<div class="srcpanel" id="src-%s" data-title="%s" hidden><p class="cap">%s</p>%s</div>`,
		esc(id), esc(title), esc(caption), rendered) + "\n"
}

func chip(kind, value string) string {
	if value == "" {
		return `<span class="nil">—</span>`
	}
	body := fmt.Sprintf(`<span class="chip %s-%s">%s</span>`, kind, esc(value), esc(value))
	// A word the reference defines carries its meaning where it is used. Hover
	// answers on a desktop; the link answers everywhere else, which is the half
	// a tooltip alone leaves out.
	if t, ok := define(value); ok {
		return fmt.Sprintf(`<a class="term" href="#help-%s" title="%s">%s</a>`, esc(value), esc(t.Short), body)
	}
	return body
}

// pane is one tab.
type pane struct {
	id, label string
	first     bool
}

func panes(doc *Document) []pane {
	var out []pane
	add := func(id, label string) {
		out = append(out, pane{id: id, label: label, first: len(out) == 0})
	}
	// Messages first: a reader arrives asking what an 0200 carries, not what the
	// document's vocabulary means.
	if len(doc.MessageViews) > 0 {
		add("messages", "Messages")
	}
	add("elements", "Data elements")
	// The primer, the vocabulary and the sources are one thing — everything a
	// reader needs that is not the protocol's own tables.
	add("help", "Help")
	if doc.Source != "" {
		add("spec", "The spec")
	}
	return out
}

// linker turns the names a document uses into links to where they are defined.
// A reference is a web of cross-references — a message names its pair, a
// condition names an element, an amount names its currency — and a reader who
// has to scroll back to look each one up is doing by hand what a link does.
type linker struct {
	messages map[string]bool
	fields   map[int]bool
	sets     map[string]bool
}

func newLinker(doc *Document) *linker {
	l := &linker{messages: map[string]bool{}, fields: map[int]bool{}, sets: map[string]bool{}}
	for _, t := range doc.ValueSets {
		l.sets[t.Name] = true
	}
	for _, m := range doc.MessageViews {
		l.messages[m.MTI] = true
	}
	for _, f := range doc.FieldViews {
		l.fields[f.DE] = true
	}
	return l
}

// mti links a four-digit message, when the document has a section for it.
func (l *linker) mti(code string) string {
	if !l.messages[code] {
		return esc(code)
	}
	return `<a class="xref" href="#m-` + esc(code) + `">` + esc(code) + `</a>`
}

// prose links every message named in a sentence: "A request. Its response is
// 0110." carries a reference the reader would otherwise go looking for.
func (l *linker) prose(text string) string {
	var b strings.Builder
	runes := []byte(esc(text))
	for i := 0; i < len(runes); {
		if i+4 <= len(runes) && isFourDigits(string(runes[i:i+4])) && !surroundedByDigits(runes, i, 4) {
			code := string(runes[i : i+4])
			if l.messages[code] {
				b.WriteString(l.mti(code))
				i += 4
				continue
			}
		}
		b.WriteByte(runes[i])
		i++
	}
	return b.String()
}

// mtiList links each message in an "0100, 0200" cell.
func (l *linker) mtiList(list string) string {
	parts := strings.Split(list, ", ")
	for i, p := range parts {
		parts[i] = l.mti(p)
	}
	return strings.Join(parts, ", ")
}

// condition links the elements a `when` expression reads, so the rule and the
// element it depends on are one click apart.
func (l *linker) condition(expr string) string {
	if expr == "" {
		return `<span class="nil">—</span>`
	}
	e, err := sdl.ParseWhen(expr)
	if err != nil || len(e.Refs) == 0 {
		return "<code>" + esc(expr) + "</code>"
	}
	// Rewrite from the end so earlier positions stay valid.
	out := []byte(expr)
	refs := append([]sdl.FieldRef(nil), e.Refs...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].Pos > refs[j].Pos })
	for _, r := range refs {
		if !l.fields[r.DE] {
			continue
		}
		text := r.String()
		end := r.Pos + len(text)
		if r.Pos < 0 || end > len(out) || string(out[r.Pos:end]) != text {
			continue
		}
		link := `</code><a class="xref" href="#de-` + fmt.Sprint(r.DE) + `"><code>` + esc(text) + `</code></a><code>`
		out = append(out[:r.Pos], append([]byte(link), out[end:]...)...)
	}
	return "<code>" + string(out) + "</code>"
}

// deRef links "in the minor unit of DE 49" to the element it names.
func (l *linker) deRef(text string) string {
	out := esc(text)
	for de := range l.fields {
		needle := fmt.Sprintf("DE %d", de)
		if strings.HasSuffix(out, needle) {
			return strings.TrimSuffix(out, needle) +
				fmt.Sprintf(`<a class="xref" href="#de-%d">%s</a>`, de, needle)
		}
	}
	return out
}

func surroundedByDigits(b []byte, i, n int) bool {
	if i > 0 && b[i-1] >= '0' && b[i-1] <= '9' {
		return true
	}
	if i+n < len(b) && b[i+n] >= '0' && b[i+n] <= '9' {
		return true
	}
	return false
}

// valueSetRef links a set's name to where it is documented. A name in a cell
// that leads nowhere is a word the reader has to match by hand against a table
// rendered under some other element — if it was rendered at all.
func (l *linker) valueSetRef(name string) string {
	if name == "" {
		return `<span class="nil">—</span>`
	}
	if !l.sets[name] {
		return "<code>" + esc(name) + "</code>"
	}
	return `<a class="xref" href="#values-` + esc(name) + `"><code>` + esc(name) + `</code></a>`
}

// term marks a bare word the reference defines — a layout, a value kind — the
// same way a chip is marked.
func term(word string) string {
	t, ok := define(word)
	if !ok {
		return esc(word)
	}
	return fmt.Sprintf(`<a class="term plain" href="#help-%s" title="%s">%s</a>`, esc(word), esc(t.Short), esc(word))
}

func textCell(s string) string {
	if s == "" {
		return `<span class="nil">—</span>`
	}
	return esc(s)
}

func foldedRulesHTML(row MessageRow, link *linker) string {
	if len(row.Rules) == 0 {
		if row.When == "" {
			return `<span class="nil">—</span>`
		}
		return link.condition(row.When)
	}
	parts := make([]string, 0, len(row.Rules))
	for _, rule := range row.Rules {
		if rule.Condition == "" {
			parts = append(parts, chip("u", rule.Usage)+" otherwise")
			continue
		}
		parts = append(parts, chip("u", rule.Usage)+" when "+link.condition(rule.Condition))
	}
	return strings.Join(parts, "<br>")
}

// valueTableHTML renders one value domain and the claim it makes about anything
// outside it. withHeading is false where the heading is already the section's.
func valueTableHTML(t ValueTable, withHeading bool) string {
	var b strings.Builder
	claim := "the documented set. Others may occur."
	if t.Closed {
		claim = "this list is complete; anything else is invalid."
	}
	if withHeading {
		fmt.Fprintf(&b, "<p class=\"lbl\"><strong>%s</strong> — %s</p>\n", esc(t.Heading), claim)
	} else {
		fmt.Fprintf(&b, "<p class=\"pair\">%s</p>\n", strings.ToUpper(claim[:1])+claim[1:])
	}
	b.WriteString(`<div class="tw"><table><thead><tr><th>Value</th><th>Name</th>`)
	if t.HasCat {
		b.WriteString("<th>Group</th>")
	}
	if t.HasDesc {
		b.WriteString("<th>Description</th>")
	}
	b.WriteString("</tr></thead><tbody>\n")
	for _, row := range t.Rows {
		fmt.Fprintf(&b, "<tr><td><code>%s</code></td><td>%s</td>", esc(row.Code), esc(row.Name))
		if t.HasCat {
			fmt.Fprintf(&b, "<td>%s</td>", textCell(row.Category))
		}
		if t.HasDesc {
			fmt.Fprintf(&b, "<td>%s</td>", textCell(row.Description))
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody></table></div>\n")
	return b.String()
}
