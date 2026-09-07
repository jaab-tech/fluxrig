// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
)

// A protocol reference gets mailed, attached to a ticket, and opened from a
// laptop with no network. Anything fetched is a page that renders wrong exactly
// when someone needs it.
func TestTheHTMLPageFetchesNothing(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})
	for _, pattern := range []string{`src="http`, `href="http`, `@import`, `url(http`} {
		if strings.Contains(out, pattern) {
			t.Errorf("the page reaches the network: %s", pattern)
		}
	}
}

// Anchors have to be unique and every link has to land. A duplicate id sends the
// browser to whichever copy comes first, which is how a table of contents stops
// working without anything appearing to be wrong.
func TestEveryInternalLinkLands(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopeComplete})

	ids := map[string]int{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(out, -1) {
		ids[m[1]]++
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("id %q appears %d times", id, n)
		}
	}

	links := regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(out, -1)
	if len(links) == 0 {
		t.Fatal("the page has no internal links, so this proves nothing")
	}
	for _, m := range links {
		if ids[m[1]] == 0 {
			t.Errorf("link to #%s lands nowhere", m[1])
		}
	}
}

// A spec is authored by someone else and its prose is not markup. A description
// carrying angle brackets must not become part of the page.
func TestSpecProseIsEscaped(t *testing.T) {
	spec, err := sdl.ParseSemantic([]byte(`
spec:
  id: esc
  name: "Esc & <b>co</b>"
  version: 1.0.0
  fields:
    2:
      name: "PAN <script>alert(1)</script>"
      description: "Length < 20 & > 12"
      note: "Beware of </td> in prose"
`))
	if err != nil {
		t.Fatal(err)
	}
	out := HTML(spec, Options{Scope: ScopeComplete})
	for _, raw := range []string{"<script>alert(1)</script>", "</td> in prose", "<b>co</b>"} {
		if strings.Contains(out, raw) {
			t.Errorf("unescaped prose reached the page: %q", raw)
		}
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("the field name was not escaped at all")
	}
}

// The public variant is the one that may be published, and the marking has to
// hold in the HTML exactly as it does in the Markdown.
func TestTheHTMLPublicVariantWithholdsPrivateFields(t *testing.T) {
	spec := reference(t)
	pub := HTML(spec, Options{Scope: ScopePublic})
	full := HTML(spec, Options{Scope: ScopeComplete})

	private := 0
	for de, f := range spec.Fields {
		if f.IsPublic() {
			continue
		}
		private++
		anchor := `id="de-` + itoa(de) + `"`
		if strings.Contains(pub, anchor) {
			t.Errorf("DE %d is private and appears in the public page", de)
		}
		if !strings.Contains(full, anchor) {
			t.Errorf("DE %d is missing from the complete page", de)
		}
	}
	if private == 0 {
		t.Fatal("the reference spec marks no field private")
	}
	if !strings.Contains(pub, "private elements are omitted") {
		t.Error("the public page does not say that anything was withheld")
	}
}

// A PDF is read on paper, where the reader's theme does not apply and a table
// split across sheets loses its header.
func TestThePageCanBePrinted(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})
	for _, want := range []string{
		"@media print",
		"display:table-header-group", // the header repeats on every sheet
		"page-break-inside:avoid",    // no row split across two
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the print stylesheet is missing %q", want)
		}
	}
}

// The page names what generated it and nothing else. Whoever documents their own
// protocol should not find someone else's company on their work — the generator
// is the only party with a claim to be there.
func TestThePageNamesItsGeneratorAndNoOneElse(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})

	if !strings.Contains(out, `class="logo"`) || !strings.Contains(out, `aria-label="fluxrig"`) {
		t.Error("the generator's mark is missing")
	}
	if !strings.Contains(out, "fluxrig spec doc") {
		t.Error("the page does not say what generated it")
	}
	for _, mark := range []string{"jaab", "JAAB", "jaab.tech"} {
		if strings.Contains(out, mark) {
			t.Errorf("the page carries a party that has no claim to be on it: %q", mark)
		}
	}
}

// The mark is inline for the same reason as everything else on the page: a
// reference opened from an attachment fetches nothing.
func TestTheMarkIsInlineNotFetched(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})
	if strings.Contains(out, "<img") {
		t.Error("the page loads an image, which will not travel with the file")
	}
	// The wordmark hardcodes a dark blue that vanishes on a dark ground, and a
	// fill attribute cannot be reached any other way.
	if !strings.Contains(out, "fill=\"var(--logo-word,") {
		t.Error("the wordmark's fill is not reachable, so it disappears on a dark ground")
	}
	if !strings.Contains(out, "--logo-word:#7FB3CC") {
		t.Error("nothing lightens the wordmark on a dark ground")
	}
}

// A reference is scrolled a long way down. The way back has to be one reach, and
// on a narrow screen an index of sixty entries above the content is a wall to
// scroll past rather than a table of contents.
func TestThePageCanBeNavigated(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})
	for _, want := range []string{
		`id="top"`, "Back to top", "window.scrollTo",
		`id="menu"`, `aria-expanded="false"`, "nav.classList.toggle('open'",
		"e.key === 'Escape'", // a panel that traps you is worse than no panel
	} {
		if !strings.Contains(out, want) {
			t.Errorf("navigation is missing %q", want)
		}
	}
	// Nothing that only exists to be clicked belongs in a PDF.
	if !strings.Contains(out, ".float,.scrim,.hbtns{display:none!important}") {
		t.Error("the floating controls would print")
	}
}

// An open panel that lets the page scroll behind it loses the reader's place in
// the document they opened it to navigate. Two things are needed and neither is
// enough alone: the panel must not hand its scroll to the page when it reaches
// its end, and the page must not scroll while the panel is over it.
func TestAnOpenMenuDoesNotScrollThePageBehindIt(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})

	if !strings.Contains(out, "overscroll-behavior:contain") {
		t.Error("reaching the end of the index would scroll the page behind it")
	}
	if !strings.Contains(out, ":root.navopen{overflow:hidden}") {
		t.Error("the page is not held still while the panel is open")
	}
	// The panel must keep its own scroll. Freezing the page by repositioning the
	// body takes the panel's scroll with it and sends the reader to the top —
	// two failures worse than the one being fixed.
	if strings.Contains(out, "position:fixed;left:0;right:0;width:100%") {
		t.Error("the page is frozen by repositioning the body, which breaks the panel")
	}
	if !strings.Contains(out, "overflow-y:auto;-webkit-overflow-scrolling:touch") {
		t.Error("the panel has no scroll of its own")
	}
	// A window widened past the breakpoint turns the panel back into a sidebar,
	// and a body still frozen for it cannot scroll at all.
	if !strings.Contains(out, "if (window.innerWidth > 880) setMenu(false)") {
		t.Error("a resize past the breakpoint would leave the page frozen")
	}
}

// Both formats render one document. If they read the spec separately they would
// answer the same questions separately, and differ the moment one changed.
func TestBothFormatsDescribeTheSameThing(t *testing.T) {
	doc := Build(reference(t), Options{Scope: ScopeComplete})
	md, page := MarkdownDoc(doc), HTMLDoc(doc)

	for _, m := range doc.MessageViews {
		if !strings.Contains(md, "### "+m.MTI+" — ") {
			t.Errorf("markdown is missing message %s", m.MTI)
		}
		if !strings.Contains(page, `id="m-`+m.MTI+`"`) {
			t.Errorf("html is missing message %s", m.MTI)
		}
	}
	for _, f := range doc.FieldViews {
		if !strings.Contains(md, "### DE "+itoa(f.DE)+" — ") {
			t.Errorf("markdown is missing DE %d", f.DE)
		}
		if !strings.Contains(page, `id="de-`+itoa(f.DE)+`"`) {
			t.Errorf("html is missing DE %d", f.DE)
		}
	}
}

// The mark has to be small and it has to stay small. Its SVG carried an
// intrinsic 1250×450, so a single lost CSS rule drew it a metre wide across the
// top of the reference — which is what happened.
func TestTheMarkIsSizedAndCannotEscapeIt(t *testing.T) {
	out := HTML(reference(t), Options{Scope: ScopePublic})

	if strings.Contains(out, `width="1250"`) || strings.Contains(out, `height="450"`) {
		t.Error("the logo carries an intrinsic size, so it renders huge without CSS")
	}
	if !strings.Contains(out, ".logo{height:") {
		t.Error("nothing constrains the logo on screen")
	}
	if !strings.Contains(out, ".logo{height:15pt}") {
		t.Error("nothing constrains the logo in print")
	}
}

// A printed reference outlives the deployment it described, and the browser
// prints the file's path where a document would print its own name. Without the
// spec's version on the page, the reader cannot tell which protocol they are
// holding.
func TestAPrintedReferenceIdentifiesItself(t *testing.T) {
	spec := reference(t)
	out := HTML(spec, Options{Scope: ScopePublic})

	if spec.Version == "" {
		t.Fatal("the reference spec declares no version, so this proves nothing")
	}
	if !strings.Contains(out, "Version "+spec.Version) {
		t.Error("the version is not on the page")
	}
	if !strings.Contains(out, `class="printonly"`) {
		t.Error("nothing identifies the document on paper")
	}
	if !strings.Contains(out, ".printonly{display:none}") {
		t.Error("the print-only line shows on screen too")
	}
	if !strings.Contains(out, "public variant") {
		t.Error("the printed page does not say which variant it is")
	}
}

// A reference is a derived view. A reader who doubts a table should find the
// text it came from under it, not have to go looking for the file — and it has
// to be the file, not a re-serialisation, or the fragment is a claim about the
// source rather than the source.
func TestEverySectionCarriesTheYAMLItCameFrom(t *testing.T) {
	raw, err := os.ReadFile(refSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := sdl.ParseSemantic(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	for _, want := range []string{
		`data-src="yaml-39"`,   // a field's own YAML, beside its title
		`data-src="wire-39"`,   // and the wire layer it resolves to
		`data-src="msg-0100"`,  // a message
		`data-src="vals-39-0"`, // a value set
		`<div class="yaml">`,   // the whole spec, coloured and folded
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no source is offered for %q", want)
		}
	}

	// The fragment is lifted, so what the author wrote is what appears —
	// including a comment that a re-serialisation would drop.
	if !strings.Contains(out, "Custom extension: proprietary, excluded from public documentation.") {
		t.Error("a comment written above a field did not survive into its fragment")
	}
	// And it is escaped like everything else: a spec is written by someone else.
	if strings.Contains(out, "<pre><code>spec:\n  id: <") {
		t.Error("the fragment is not escaped")
	}

	// Without a source the page is still a reference, and offers no fragments
	// rather than empty ones.
	bare := HTML(spec, Options{Scope: ScopeComplete})
	if strings.Contains(bare, "class=\"srcbtn\"") || strings.Contains(bare, "details class=\"src\"") {
		t.Error("a page with no source offers empty fragments")
	}
}

// Printing every fragment would treble a document to say what it already said.
// What the reader opened is what they meant to keep.
func TestOnlyOpenedFragmentsPrint(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, "details.src:not([open]){display:none}") {
		t.Error("closed fragments would print")
	}
}

// A reference explains the vocabulary it uses; it cannot explain the protocol.
// Sending the reader to a standards document for that is sending them to buy
// one, so the spec carries its own primer and the reference renders it.
func TestTheDocumentExplainsItsProtocol(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, err := sdl.ParseSemantic(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Overview == "" {
		t.Fatal("the reference spec carries no overview, so this proves nothing")
	}
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, ">Introduction</h2>") {
		t.Error("the primer is not rendered")
	}
	// Authored prose is escaped and paragraphed, never interpreted: a spec is
	// written by someone else, and prose that becomes markup rewrites the page.
	if !strings.Contains(out, "<p>An ISO 8583 message is a message type indicator") {
		t.Error("the primer is not broken into paragraphs")
	}
}

// A protocol reference that cannot be traced to a normative source is an
// assertion. The sources here are sold by a standards body or issued under a
// scheme's terms, so the document cites them and says how to obtain one — which
// is the alternative to carrying a copy nobody has the right to distribute.
func TestTheDocumentCitesItsSources(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	if len(spec.References) == 0 {
		t.Fatal("the reference spec cites nothing")
	}
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, ">References</h2>") {
		t.Error("the references are not rendered")
	}
	for _, r := range spec.References {
		if !strings.Contains(out, r.Title) {
			t.Errorf("reference %q is missing", r.Title)
		}
		// How to obtain it is the part a reader needs when the document is sold.
		if r.Note != "" && !strings.Contains(out, r.Note) {
			t.Errorf("reference %q does not say how to obtain it", r.Title)
		}
	}
	if !strings.Contains(out, "cited, not carried") {
		t.Error("the page does not say why the sources are not included")
	}
}

// Printed, there is no sidebar and no search: a reader looking for DE 39 turns
// pages until they find it.
func TestThePrintedDocumentHasContents(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, `class="printonly toc"`) {
		t.Error("there is no contents page")
	}
	if !strings.Contains(out, ">Contents</h2>") {
		t.Error("the contents page has no heading")
	}
	// It belongs to paper only: on screen the sidebar is already there.
	if !strings.Contains(out, ".printonly{display:none}") {
		t.Error("the contents page shows on screen as well")
	}
	if !strings.Contains(out, ".toc{page-break-after:always") {
		t.Error("the contents run into the document instead of ending the page")
	}
}

// A reference is a web of cross-references: a message names its pair, a rule
// names the element it depends on, an amount names its currency. A reader who
// has to scroll back to look each one up is doing by hand what a link does.
func TestTheDocumentIsCrossLinked(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	// "A request. Its response is 0110." — the pair is a link.
	if !strings.Contains(out, `Its response is <a class="xref" href="#m-0110">0110</a>`) {
		t.Error("a message's pair is not linked")
	}
	// A condition names the element it reads, inside the expression.
	if !strings.Contains(out, `<a class="xref" href="#de-22"><code>22</code></a>`) {
		t.Error("a condition does not link the element it reads")
	}
	// An amount names the element carrying its currency.
	if !strings.Contains(out, `<a class="xref" href="#de-49">DE 49</a>`) {
		t.Error("an amount does not link its currency element")
	}
	// In a message table, both the number and the name lead to the element.
	if !strings.Contains(out, `<td class="n"><a class="xref" href="#de-39">39</a></td>`) {
		t.Error("an element's number in a message table is not linked")
	}
	if !strings.Contains(out, `<td><a class="xref" href="#de-39">Response Code</a></td>`) {
		t.Error("an element's name in a message table is not linked")
	}
	// Every link still has to land.
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(out, -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(out, -1) {
		if !ids[m[1]] {
			t.Errorf("link to #%s lands nowhere", m[1])
		}
	}
}

// A word the reference defines is explained where it is used, not in a preamble
// the reader passed twenty pages ago. Hover answers on a desktop; the link
// answers on a phone, where nothing hovers.
func TestEveryDefinedWordCarriesItsMeaning(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	for _, word := range []string{"mandatory", "forbidden", "echo", "modified", "tlv", "amount", "pan"} {
		if !strings.Contains(out, `href="#help-`+word+`"`) {
			t.Errorf("%q is used but never links to its meaning", word)
		}
		if !strings.Contains(out, `id="help-`+word+`"`) {
			t.Errorf("%q has no entry in the help section", word)
		}
	}
	// Hover carries the short form, so a desktop reader does not lose their place.
	if !strings.Contains(out, `title="The element must be present."`) {
		t.Error("a defined word offers no meaning on hover")
	}
}

// Sixty elements read as one page is a page nobody navigates. Each section is a
// pane — and a PDF has no tabs, so printing shows them all.
func TestThePageIsTabbedAndPrintsWhole(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	// A reader arrives asking what an 0200 carries, so Messages is where they
	// land. The primer, the vocabulary and the sources are one tab: they are
	// everything that is not the protocol's own tables.
	for _, id := range []string{"messages", "elements", "help", "spec"} {
		if !strings.Contains(out, `data-pane="`+id+`"`) {
			t.Errorf("there is no %q pane", id)
		}
	}
	first := strings.Index(out, `data-pane="messages"`)
	for _, later := range []string{`data-pane="elements"`, `data-pane="help"`, `data-pane="spec"`} {
		if i := strings.Index(out, later); i >= 0 && i < first {
			t.Errorf("%s comes before Messages, so it is what a reader lands on", later)
		}
	}
	// The three merged sections are still reachable, inside the help tab.
	for _, anchor := range []string{`id="intro"`, `id="help"`, `id="refs"`} {
		if !strings.Contains(out, anchor) {
			t.Errorf("%s was lost in the merge", anchor)
		}
	}
	if !strings.Contains(out, `.pane[hidden]{display:none}`) {
		t.Error("panes are not folded away")
	}
	if !strings.Contains(out, `.pane[hidden]{display:block!important}`) {
		t.Error("a printed document would be missing every pane but the first")
	}
	// A link into a folded pane has to open it, or half the cross-references in
	// this document lead somewhere invisible.
	if !strings.Contains(out, "function paneOf(id)") || !strings.Contains(out, "window.addEventListener('hashchange'") {
		t.Error("a deep link does not open the pane it points into")
	}
}

// The spec is a thousand lines. As one block it is a block nobody reads, so it
// is coloured and folded — and written here rather than pulled from a
// highlighting library, because the page fetches nothing.
func TestTheSpecPaneIsReadable(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	for _, want := range []string{
		`class="yc-key"`,      // keys
		`class="yc-str"`,      // quoted values
		`class="yc-com"`,      // comments the author wrote
		`<details class="yf"`, // folds
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the spec pane is missing %q", want)
		}
	}
	// A fold that cannot be opened on paper has to print open, or the printed
	// document carries a spec with its contents missing.
	if !strings.Contains(out, ".yaml details.yf>.yc{display:block!important}") {
		t.Error("folded sections would print empty")
	}
	// It is still the author's text: a comment is coloured, not dropped.
	if !strings.Contains(out, "binds amount to its currency") {
		t.Error("a comment did not survive highlighting")
	}
}

// The element's own wire layer, beside its title. What the spec wrote is often
// only a delta over a named base, which says almost nothing about how the bytes
// are read.
func TestEachElementShowsTheWireItResolvesTo(t *testing.T) {
	const p = "../../../examples/specs/iso8583-v87-ascii.yaml"
	raw, _ := os.ReadFile(p)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw, BaseDir: filepath.Dir(p)})

	if !strings.Contains(out, `id="src-wire-4"`) {
		t.Fatal("DE 4 offers no wire layer")
	}
	// The reference spec names a Moov base and overrides only the padding, so
	// everything else here came from the base — which is the point.
	wire := out[strings.Index(out, `id="src-wire-4"`):]
	// The panel is nested now, so cut at the next panel rather than the first
	// closing tag inside this one.
	if end := strings.Index(wire[1:], `class="srcpanel"`); end > 0 {
		wire = wire[:end]
	}
	for _, want := range []string{"length", "12", "ASCII.Fixed", "padding"} {
		if !strings.Contains(wire, want) {
			t.Errorf("DE 4's wire layer does not show %q", want)
		}
	}
	// And each wire word links to what it does to the bytes.
	if !strings.Contains(out, `href="#help-ASCII.Fixed"`) {
		t.Error("a length prefix does not link to its meaning")
	}
	if !strings.Contains(out, `href="#help-String"`) {
		t.Error("a wire type does not link to its meaning")
	}
}

// The mark goes home, and home is wherever the reader lands. The anchor is real
// rather than handled only in script: a link that needs JavaScript to resolve is
// a link that breaks when the page is saved and reopened somewhere stricter.
func TestTheMarkGoesHome(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	first := panes(Build(spec, Options{Scope: ScopePublic}))[0].id
	if first != "messages" {
		t.Fatalf("home is %q; a reader arrives asking what a message carries", first)
	}
	if !strings.Contains(out, `<a class="home" id="home" href="#pane-messages"`) {
		t.Error("the mark does not lead home")
	}
	if !strings.Contains(out, `id="pane-messages"`) {
		t.Error("home has no anchor, so the link only works while the script does")
	}
	// A hash already set fires no hashchange, so the mark answers a click of its
	// own — or it works once and never again.
	if !strings.Contains(out, "home.addEventListener('click'") {
		t.Error("the mark stops working after the first use")
	}
}

// A fragment numbered from one is a fragment the reader then has to go and find
// in the file. The numbers are the document's own, so a line here is the line to
// open the editor at.
func TestFragmentsAreNumberedAgainstTheFile(t *testing.T) {
	raw, err := os.ReadFile(refSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	// Where DE 39 actually starts in the file.
	want := 0
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "39:" {
			want = i + 1
			break
		}
	}
	if want == 0 {
		t.Fatal("DE 39 is not in the reference spec, so this proves nothing")
	}

	panel := out[strings.Index(out, `id="src-yaml-39"`):]
	panel = panel[:strings.Index(panel, "</div></div>")]
	first := regexp.MustCompile(`class="yn" aria-hidden="true">(\d+)<`).FindStringSubmatch(panel)
	if first == nil {
		t.Fatal("DE 39's fragment carries no line numbers")
	}
	if first[1] != itoa(want) {
		t.Errorf("DE 39's fragment starts at line %s; in the file it is line %d", first[1], want)
	}
	if !strings.Contains(out, "From line "+itoa(want)+" of the spec document.") {
		t.Error("the panel does not say which line it came from")
	}

	// The whole spec is numbered from its own first line.
	if !strings.Contains(out, `class="yn" aria-hidden="true">1<`) {
		t.Error("the spec pane does not start at line 1")
	}
	// A wire layer is assembled from a base and a delta, so it belongs to no
	// line of any file and must not be numbered as if it did.
	wire := out[strings.Index(out, `id="src-wire-4"`):]
	// The panel is nested now, so cut at the next panel rather than the first
	// closing tag inside this one.
	if end := strings.Index(wire[1:], `class="srcpanel"`); end > 0 {
		wire = wire[:end]
	}
	if strings.Contains(wire, `class="yn"`) {
		t.Error("the wire layer is numbered against a file it did not come from")
	}
}

// The number is a gutter. Copying a block has to yield YAML, not a column of
// digits down its left.
func TestLineNumbersAreNotCopied(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, "user-select:none") {
		t.Error("the numbers would be copied along with the YAML")
	}
	if !strings.Contains(out, `class="yn" aria-hidden="true"`) {
		t.Error("the numbers are read out as content by a screen reader")
	}
}

// Panes are siblings, in the order the tabs offer them.
//
// They were not: the help pane opened first and closed last, so Messages and
// Data elements were nested inside it. Hiding help hid them both, and selecting
// Messages lit the right tab over an empty page. None of the tests noticed,
// because every one of them asked whether a pane existed and none asked where.
func TestPanesAreSiblingsInTabOrder(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	body := out[strings.Index(out, "<main>"):]
	body = body[:strings.Index(body, "</main>")]

	// Walk every section tag, not only the panes: the printed contents page is
	// one too, and counting only half of them makes the depth meaningless.
	depth, order := 0, []string(nil)
	paneID := regexp.MustCompile(`id="pane-([a-z]+)"`)
	for _, tag := range regexp.MustCompile(`<section[^>]*>|</section>`).FindAllString(body, -1) {
		if strings.HasPrefix(tag, "</") {
			depth--
			continue
		}
		if m := paneID.FindStringSubmatch(tag); m != nil {
			if depth != 0 {
				t.Fatalf("pane %q opens inside another section", m[1])
			}
			order = append(order, m[1])
		}
		depth++
	}
	if depth != 0 {
		t.Errorf("panes are unbalanced: %d left open", depth)
	}

	// And they appear in the order the tabs offer them, which is the order a
	// printed document reads in.
	var tabs []string
	for _, m := range regexp.MustCompile(`data-pane="([a-z]+)" aria-selected`).FindAllStringSubmatch(out, -1) {
		tabs = append(tabs, m[1])
	}
	if strings.Join(order, ",") != strings.Join(tabs, ",") {
		t.Errorf("panes are in %v; the tabs offer %v", order, tabs)
	}
}

// Every pane has to hold what its tab promises. An empty pane behind a lit tab
// reads as a document with nothing in it.
func TestNoPaneIsEmpty(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	for _, id := range []string{"messages", "elements", "help", "spec"} {
		start := strings.Index(out, `id="pane-`+id+`"`)
		if start < 0 {
			t.Errorf("there is no %q pane", id)
			continue
		}
		rest := out[start:]
		end := strings.Index(rest, `<section class="pane"`)
		if end < 0 {
			end = strings.Index(rest, "</main>")
		}
		if end < 0 {
			end = len(rest)
		}
		if body := rest[:end]; len(body) < 400 || !strings.Contains(body, "<h2") {
			t.Errorf("the %q pane holds nothing: %d bytes", id, len(body))
		}
	}
}

// A fragment opens in one dialog rather than in a box under its element. Sixty
// boxes are sixty things a reader scrolls past to reach the next element.
func TestFragmentsOpenInADialog(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	if !strings.Contains(out, `<dialog id="srcdlg">`) {
		t.Fatal("there is no dialog")
	}
	// The panels live out of the flow, so nothing sits under an element.
	store := strings.Index(out, `<div id="srcstore" hidden>`)
	if store < 0 {
		t.Fatal("the fragments are not held out of the flow")
	}
	elements := out[strings.Index(out, `id="pane-elements"`):store]
	if strings.Contains(elements, `class="srcpanel"`) {
		t.Error("a fragment is still rendered inline under its element")
	}
	// Every button has a panel, and every panel a button.
	buttons := regexp.MustCompile(`data-src="([^"]+)"`).FindAllStringSubmatch(out, -1)
	if len(buttons) < 100 {
		t.Errorf("only %d fragments are offered", len(buttons))
	}
	for _, m := range buttons {
		if !strings.Contains(out, `id="src-`+m[1]+`"`) {
			t.Errorf("the button for %q opens nothing", m[1])
		}
	}
	// Escape and the backdrop close it; a native dialog gives the first for free.
	if !strings.Contains(out, "showModal") || !strings.Contains(out, "e.target === dlg") {
		t.Error("the dialog cannot be dismissed")
	}
	// It cannot be opened on paper, and the whole spec prints in its own section.
	if !strings.Contains(out, "#srcstore,dialog#srcdlg{display:none!important}") {
		t.Error("the fragments would print, repeating the spec sixty times")
	}
}

// The introduction is prose at the top of the help pane: nothing folds it and
// nothing hides it, so a reader who opens Help has already arrived at it.
func TestTheIntroductionIsNotFolded(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, `<div class="sect" data-sec="intro">`) {
		t.Fatal("the introduction is not a section of the help pane")
	}
	sect := out[strings.Index(out, `data-sec="intro"`):]
	sect = sect[:strings.Index(sect, `data-sec="words"`)]
	if strings.Contains(sect, `<details class="more">`) {
		t.Error("the introduction is folded")
	}
	if strings.Contains(sect, "hidden") {
		t.Error("the introduction starts hidden")
	}
	// Every paragraph the spec wrote is there, not only the first.
	if n := strings.Count(sect, "<p>"); n < 5 {
		t.Errorf("the introduction shows %d paragraphs; the spec writes more", n)
	}
}

// A word explains itself where it stands. Following the link would land the
// reader in the glossary having lost the table they were reading.
func TestADefinedWordExplainsItselfWithoutLeaving(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	// Every defined word has an entry the dialog can show.
	for _, g := range Glossary() {
		for _, term := range g.Terms {
			if !strings.Contains(out, `id="src-term-`+term.Key+`"`) {
				t.Errorf("%q has no entry the dialog can show", term.Key)
			}
		}
	}
	if !strings.Contains(out, "e.preventDefault()") || !strings.Contains(out, "openSource('term-' + key)") {
		t.Error("clicking a word still navigates away from the table")
	}
	// The link stays, so the page still works with no script and still means
	// something when it is saved and reopened somewhere stricter.
	if !strings.Contains(out, `<a class="term`) {
		t.Error("the words are no longer links, so they do nothing without script")
	}
	if !strings.Contains(out, `href="#help-mandatory"`) {
		t.Error("a word's link no longer resolves on its own")
	}
	// And the dialog offers the way to the rest of them.
	if !strings.Contains(out, "See it among the others") {
		t.Error("the dialog is a dead end")
	}
}

// The tabs stay put. A reference is scrolled a long way down, and a reader who
// must return to the top to change section is a reader who does not.
func TestTheTabsStayVisible(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, "position:sticky;top:0;z-index:30") {
		t.Error("the tabs scroll away")
	}
	// A heading jumped to must not land under the bar that is now over it.
	if !strings.Contains(out, "scroll-margin-top:66px") {
		t.Error("an anchor lands behind the tab bar")
	}
	// Printed, a bar fixed to a viewport that does not exist would sit on the
	// first page and nowhere else.
	if !strings.Contains(out, ".tabs,.tabbar{display:none!important;position:static}") {
		t.Error("the tab bar is still positioned in print")
	}
}

// A named base is not in the document, which is the point of naming one. A page
// telling the reader everything came from the file above would be telling them
// half: the bytes are read by a layout that arrived from somewhere else.
func TestTheSpecPaneShowsTheWireItResolved(t *testing.T) {
	const p = "../../../examples/specs/iso8583-v87-ascii.yaml"
	raw, _ := os.ReadFile(p)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw, BaseDir: filepath.Dir(p)})

	if !strings.Contains(out, `<h3 id="written">The spec, as written</h3>`) {
		t.Error("the spec pane does not separate the document from what it resolved to")
	}
	if !strings.Contains(out, `<h3 id="wire">The wire layer, resolved</h3>`) {
		t.Fatal("the resolved wire layer is not shown")
	}
	// It says where the layout came from, since the answer is not in the file.
	if !strings.Contains(out, "<code>moov:spec87ascii</code>") {
		t.Error("the page does not name the base the layout came from")
	}
	if !strings.Contains(out, "Not part of the document above") {
		t.Error("the page implies the wire layer is in the file")
	}
	// The base carries the whole 1987 field set, so the block is the resolved
	// result rather than the handful of overrides the spec wrote.
	pane := out[strings.Index(out, "The wire layer, resolved"):]
	if n := strings.Count(pane, `class="yc-key"`); n < 300 {
		t.Errorf("the wire block holds %d keys; it should carry the resolved base", n)
	}
}

// The mark is the brand's own file, drawn twice — in the header and in the bar
// that stays put. Two copies of one SVG claim the same ids, gradient included,
// so each instance carries its own.
func TestTheMarkIsTheFileAndIsDrawnTwice(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if n := strings.Count(out, `aria-label="fluxrig"`); n < 2 {
		t.Errorf("the mark is drawn %d times; the header and the bar both carry it", n)
	}
	// The wordmark comes from the file, not from anything reassembled here.
	if !strings.Contains(out, "fluxrig") || !strings.Contains(out, "paint0_linear_flux") {
		t.Error("the mark is not the brand's own drawing")
	}
	// Each instance owns its ids, or the second gradient claims the first's.
	if !strings.Contains(out, `id="paint0_linear_flux"`) || !strings.Contains(out, `id="paint0_linear_flux-bar"`) {
		t.Error("two copies share their ids")
	}
	if !strings.Contains(out, "url(#paint0_linear_flux-bar)") {
		t.Error("an instance's gradient reference does not follow its own id")
	}
	// Once the header has scrolled away, the bar is the only way back on screen.
	if !strings.Contains(out, `class="home tabhome"`) {
		t.Error("the bar that stays put carries no way home")
	}
	if !strings.Contains(out, ".tabhome .logo{height:17px}") {
		t.Error("the mark in the bar is not sized down for it")
	}
}

// Tabs choose the pane; the index chooses the place within it. A second row of
// tabs would be a third way to move through a document that already has two.
func TestOneIndexServesThePaneOnScreen(t *testing.T) {
	const p = "../../../examples/specs/iso8583-v87-ascii.yaml"
	raw, _ := os.ReadFile(p)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw, BaseDir: filepath.Dir(p)})

	// Every section of every pane is reachable, and none of them hides another.
	for _, sec := range []string{"intro", "words", "refs", "written", "wire"} {
		if !strings.Contains(out, `data-sec="`+sec+`"`) {
			t.Errorf("there is no %q section", sec)
		}
	}
	if strings.Contains(out, "subpane") || strings.Contains(out, "subtab") {
		t.Error("a second row of tabs survives")
	}
	if strings.Contains(out, `nav class="jump"`) {
		t.Error("a third way to move through the glossary survives")
	}

	// Each index entry declares the pane it belongs to, and the JS shows only
	// those: a panel listing sixty data elements beside an open glossary
	// answers a question nobody asked.
	for _, pane := range []string{"messages", "elements", "help", "spec"} {
		if !strings.Contains(out, `data-pane="`+pane+`"><span class="k">`) &&
			!strings.Contains(out, `data-pane="`+pane+`" href=`) &&
			!strings.Contains(out, `class="grp" data-pane="`+pane+`"`) {
			t.Errorf("the index has nothing for pane %q", pane)
		}
	}
	if !strings.Contains(out, "function indexFor(id)") ||
		!strings.Contains(out, "n.dataset.pane !== id") {
		t.Fatal("the index does not follow the pane")
	}
	if !strings.Contains(out, "nav [data-pane][hidden]{display:none}") {
		t.Error("the entries for the other panes would still show")
	}

	// Every index entry points at something that exists.
	for _, m := range regexp.MustCompile(`<a[^>]*data-pane="[^"]*" href="#([^"]+)"`).FindAllStringSubmatch(out, -1) {
		if !strings.Contains(out, `id="`+m[1]+`"`) {
			t.Errorf("the index points at #%s, which is nowhere in the page", m[1])
		}
	}
}

// A reader asking what an element is on the wire should be told, not sent to
// open a panel for it.
func TestEachElementShowsItsWireShape(t *testing.T) {
	const p = "../../../examples/specs/iso8583-v87-ascii.yaml"
	raw, _ := os.ReadFile(p)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw, BaseDir: filepath.Dir(p)})

	de4 := out[strings.Index(out, `id="de-4"`):]
	de4 = de4[:strings.Index(de4, `id="de-5"`)]
	for _, want := range []string{"Wire type", "Length", "Encoding", "Length prefix", "Padding"} {
		if !strings.Contains(de4, want) {
			t.Errorf("DE 4's section does not say its %s", want)
		}
	}
	// And each of those words leads to what it does to the bytes.
	if !strings.Contains(de4, `href="#help-String"`) {
		t.Error("the wire type is not linked to its meaning")
	}
	if !strings.Contains(de4, `href="#help-ASCII.Fixed"`) {
		t.Error("the length prefix is not linked to its meaning")
	}
	// It reports what the block says and nothing more: a bitmap declares no
	// prefix of its own, and inventing one here would invent one for the parser.
	de1 := out[strings.Index(out, `id="de-1"`):]
	de1 = de1[:strings.Index(de1, `id="de-2"`)]
	if strings.Contains(de1, "Padding") {
		t.Error("a padding was reported for an element that declares none")
	}
}

// A set's name in a cell has to lead to what it means. Without this it is a word
// the reader matches by hand against a table rendered under some other element —
// if it was rendered at all: a set reached only through a composite's part was
// named everywhere and documented nowhere.
func TestEveryValueSetIsDocumentedOnceAndLinkedTo(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopeComplete, Source: raw})

	if len(spec.Enums) == 0 {
		t.Fatal("the reference spec declares no value sets")
	}
	for name := range spec.Enums {
		if !strings.Contains(out, `id="values-`+name+`"`) {
			t.Errorf("the set %q is documented nowhere", name)
		}
	}

	// DE 55 reaches currency_iso4217 only through a part, and that mention has
	// to lead to the set like any other.
	de55 := out[strings.Index(out, `id="de-55"`):]
	de55 = de55[:strings.Index(de55, `id="de-56"`)]
	if !strings.Contains(de55, `href="#values-currency_iso4217"`) {
		t.Error("a set named in a composite's part does not lead to its meaning")
	}

	// And the set's entry says which elements it governs, so it is not a table
	// floating free of the document.
	entry := out[strings.Index(out, `id="values-currency_iso4217"`):]
	entry = entry[:strings.Index(entry, "</table>")]
	if !strings.Contains(entry, "Governs") || !strings.Contains(entry, `href="#de-49"`) {
		t.Error("the set's entry does not say where it applies")
	}
}

// Eight groups of words is a page to scroll before reaching the one asked
// about, and a reader arrives at the glossary already looking for something.
func TestTheGlossaryCanBeNavigated(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	groups := Glossary()
	if len(groups) < 5 {
		t.Fatalf("only %d groups of words; this proves little", len(groups))
	}
	for _, g := range groups {
		// Nested under "The words used here", in the one index the page has.
		if !strings.Contains(out, `<a class="sub" data-pane="help" href="#`+g.Anchor()+`"`) {
			t.Errorf("the index has no way to reach %q", g.Name)
		}
		if !strings.Contains(out, `id="`+g.Anchor()+`"`) {
			t.Errorf("%q has no anchor to be reached at", g.Name)
		}
	}
	// The value sets are reached the same way, being asked about the same way.
	if !strings.Contains(out, `data-pane="help" href="#values-currency_iso4217"`) {
		t.Error("the index does not reach the value sets")
	}
	if !strings.Contains(out, "nav a.sub{padding-left:26px") {
		t.Error("nested entries do not read as belonging to the one above them")
	}
}

// The index sits on the right, and the two viewports agree about which side that
// is: the panel already slid in from the right on a narrow screen while the
// desktop layout kept it on the left.
func TestTheIndexSitsOnTheRight(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, "grid-template-columns:minmax(0,1fr) 232px") {
		t.Error("the content column does not come first")
	}
	if !strings.Contains(out, "nav{grid-column:2;grid-row:1;") {
		t.Error("the index is not placed in the second column")
	}
	if !strings.Contains(out, "main{grid-column:1;grid-row:1;") {
		t.Error("the content is not placed in the first column")
	}
	// Placed, not reordered: the index stays first in the document, which is
	// where a reader not using the layout wants it.
	if strings.Index(out, `<nav aria-label="Contents"`) > strings.Index(out, "<main>") {
		t.Error("the index was moved in the markup rather than placed by the grid")
	}
	// And the narrow-screen panel still comes from the right.
	if !strings.Contains(out, "inset:0 0 0 auto") || !strings.Contains(out, "transform:translateX(100%)") {
		t.Error("the panel no longer slides in from the right")
	}
}

// The index said where a reader could go and never where they are. Hovering
// showed a colour, and the pointer is wherever the hand left it -- often nowhere
// near the section on screen.
func TestTheIndexMarksTheSectionTheReaderIsIn(t *testing.T) {
	raw, _ := os.ReadFile(refSpec)
	spec, _ := sdl.ParseSemantic(raw)
	out := HTML(spec, Options{Scope: ScopePublic, Source: raw})

	if !strings.Contains(out, `nav a[aria-current="location"]{`) {
		t.Fatal("there is no style for the section the reader is in")
	}
	// Distinct from hover, or the two states cannot be told apart.
	marked := out[strings.Index(out, `nav a[aria-current="location"]{`):]
	marked = marked[:strings.Index(marked, "}")]
	if !strings.Contains(marked, "background:var(--accent-soft)") {
		t.Error("the marker only recolours, which is what hover already does")
	}

	for _, want := range []string{
		"function markSection()",
		"function collectSpy()",
		`setAttribute('aria-current', 'location')`,
		"window.addEventListener('scroll', markSection",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the marker does not follow the reader: %q is missing", want)
		}
	}

	// Sixty data elements do not fit in the panel, so a marker the reader has to
	// scroll to find marks nothing.
	if !strings.Contains(out, "function keepVisible(link)") {
		t.Error("the marked entry is not kept on screen")
	}

	// The spy has to be defined before the first pane is shown: `var spyTargets`
	// running afterwards would wipe what that first showPane collected.
	if strings.Index(out, "var spyTargets") > strings.Index(out, "function showPane") {
		t.Error("the spy is initialised after the pane that would fill it")
	}

	// A pane change replaces the index, so what the marker follows changes too.
	pane := out[strings.Index(out, "function showPane"):]
	pane = pane[:strings.Index(pane, "function paneOf")]
	if !strings.Contains(pane, "collectSpy()") {
		t.Error("switching panes leaves the marker following the old index")
	}
}
