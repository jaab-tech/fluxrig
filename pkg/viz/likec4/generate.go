// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package likec4 generates a LikeC4 architecture model from a Scenario, so a
// topology can be explored interactively (drill-down per rack and per gear)
// before it is applied. The output is plain DSL text; rendering is done by
// external tooling and is not a concern of this package.
package likec4

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// ManifestLookup returns a gear type's manifest, when this build knows it. The
// generator prefers the manifest (ADR 0045) for classification, doc links, and
// I/O detection; when a type is unknown (a cross-version or future gear), it
// falls back to name-prefix heuristics, so the visualizer stays lenient.
type ManifestLookup func(gearType string) (sdk.Manifest, bool)

// element kinds emitted in the specification block. Gears are classified by
// their type prefix so each family gets a distinct shape/color.
const (
	kindZone     = "zone"
	kindRack     = "rack"
	kindIO       = "io_gear"
	kindCodec    = "codec_gear"
	kindLogic    = "logic_gear"
	kindMissing  = "missing_gear"
	kindExternal = "external"
	kindMixer    = "mixer"
)

// zoneLabelKey is the rack label whose value groups racks into visual zones
// (e.g. labels: {region: east} puts the rack inside a "region: east" zone).
// A rack's viz.group overrides it with an explicit zone name.
const zoneLabelKey = "region"

// gearNode is one resolved gear (or a placeholder for a wire endpoint that
// references a gear the scenario does not define).
type gearNode struct {
	id      string // sanitized LikeC4 identifier
	name    string // original gear name
	gtype   string // scenario gear type ("" for placeholders)
	kind    string // LikeC4 element kind
	docURL  string // public documentation URL for this gear type
	isIO    bool   // an I/O terminus (bridges an external socket)
	parent  string // container id ("" when the model has a single implicit scope)
	doc     string
	raw     string   // this gear's own YAML portion
	tags    []string // viz tags
	missing bool
	loops   []string // self-wire annotations ("out -> in")
}

// wireEdge is one unidirectional wire between two gears.
type wireEdge struct {
	fromGear, fromPort string
	toGear, toPort     string
	raw                string   // this wire's own YAML portion
	tags               []string // viz tags
	loop               bool     // self-wire (e.g. an I/O gear's echo loopback)
}

type container struct {
	id     string
	title  string
	desc   string
	raw    string   // this rack/group's own YAML portion
	zone   string   // enclosing zone id ("" for none)
	tags   []string // label-derived tags
	isRack bool     // true for racks/groups (they hold a Snake connection)
}

type zone struct {
	id    string
	title string
}

// extNode is a synthetic element for the external side of an I/O gear's
// socket: every io_* gear bridges exactly one bidirectional socket, and the
// scenario config knows the address, so the model can show where traffic
// enters and leaves each Rack.
// socketEdge is a socket whose two ends are both in this scenario: a client
// gear dialling an address a server gear in the same file listens on.
//
// Without this, each end produced its own "external" box and the same socket was
// drawn twice, once per side, with the counterparty shown as an anonymous
// outsider. In a multi-rack scenario that is most of the diagram.
type socketEdge struct {
	from  string // client gear name
	to    string // server gear name
	label string
}

type extNode struct {
	id      string
	title   string
	desc    string
	gear    string // owning gear name
	inbound bool   // server mode: external dials the gear
	label   string // relationship label (socket address)
}

// model is the intermediate graph the generator renders from.
type model struct {
	scenarioName string
	zones        []zone
	containers   []container
	gears        map[string]*gearNode // by original gear name
	order        []string             // gear names in declaration order
	wires        []wireEdge
	externals    []extNode
	sockets      []socketEdge // client gear -> server gear, both in this scenario
	mixerID      string       // "" when the scenario declares no racks
	mixerZone    string       // zone id hosting the Mixer, "" = outside every zone
	problems     []string     // human-readable validation notes
}

const (
	globalContainerID  = "global_gears"
	missingContainerID = "unresolved"
)

// Source carries the original scenario document, embedded verbatim into the
// index view's description as reference/help for the reader.
type Source struct {
	Name string // file name shown in the help text
	YAML []byte // raw scenario document
}

// Generate renders a complete LikeC4 model (specification, model, views) for
// the scenario as a single DSL document. It never fails on a merely broken
// topology: unknown wire endpoints become visible "missing" placeholder
// elements, and the accompanying notes report every irregularity found, so
// the visualization doubles as validation. src may be nil.
func Generate(sc *Scenario, src *Source, manifestFor ManifestLookup) (string, []string, error) {
	if sc == nil {
		return "", nil, fmt.Errorf("likec4: nil scenario")
	}
	if manifestFor == nil {
		manifestFor = func(string) (sdk.Manifest, bool) { return sdk.Manifest{}, false }
	}
	m := buildModel(sc, manifestFor)

	var b strings.Builder
	writeSpecification(&b, m)
	writeModel(&b, m)
	writeViews(&b, m, indexHelp(sc, src, m))
	return b.String(), m.problems, nil
}

// indexHelp builds the markdown help shown on the index view: scenario meta
// plus the full source document, so the diagram and its YAML travel together.
func indexHelp(sc *Scenario, src *Source, m *model) string {
	var h strings.Builder
	fmt.Fprintf(&h, "**Scenario** `%s` version `%s`", m.scenarioName, sc.Meta.Version)
	if src != nil && src.Name != "" {
		fmt.Fprintf(&h, " (source: `%s`)", src.Name)
	}
	h.WriteString("\n\n")
	if src == nil || len(src.YAML) == 0 {
		return h.String()
	}
	yamlText := strings.TrimRight(string(src.YAML), "\n")
	if fenced := fencedYAML(yamlText); fenced != "" {
		h.WriteString("Full definition:\n\n")
		h.WriteString(fenced)
	}
	return h.String()
}

// fencedYAML wraps a YAML snippet in a markdown code fence, or returns ""
// when the content cannot be fenced safely.
func fencedYAML(s string) string {
	if s == "" || strings.Contains(s, "````") {
		return ""
	}
	return "````yaml\n" + s + "\n````\n"
}

// markdownBlock renders s as a LikeC4 multiline markdown string, choosing a
// delimiter the content does not contain.
func markdownBlock(s string) (string, bool) {
	for _, delim := range []string{"'''", `"""`} {
		if !strings.Contains(s, delim) {
			return delim + "\n" + s + "\n" + delim, true
		}
	}
	return "", false
}

// buildModel resolves racks/groups into containers (grouped into zones by
// label or viz.group), places every gear, and normalizes wires, collecting
// problems instead of failing.
func buildModel(sc *Scenario, manifestFor ManifestLookup) *model {
	m := &model{
		scenarioName: sc.Meta.Name,
		gears:        make(map[string]*gearNode),
	}
	if m.scenarioName == "" {
		m.scenarioName = "scenario"
	}

	ids := newIDSet()
	zoneByTitle := make(map[string]string) // zone title -> zone id

	claimZone := func(title string) string {
		if id, ok := zoneByTitle[title]; ok {
			return id
		}
		id := ids.claim("zone_" + title)
		zoneByTitle[title] = id
		m.zones = append(m.zones, zone{id: id, title: title})
		return id
	}

	// Containers from declared racks and groups, in declaration order.
	containerByTarget := make(map[string]string) // deploy target -> container id
	for _, r := range sc.Racks {
		title, desc := r.Name, "Rack"
		if title == "" {
			title, desc = r.Group, "Rack group"
		}
		// A rack's name says which process it is; its role says what it does
		// there. In a scenario with several, the second is what a reader is
		// actually trying to tell apart.
		if role := r.Labels["role"]; role != "" {
			desc += ": " + strings.ReplaceAll(role, "-", " ")
		}
		if title == "" {
			continue // defaults-only entry; contributes no container
		}
		c := container{id: ids.claim(title), title: title, desc: desc, raw: r.Raw, isRack: true}

		// The zone label groups racks into a visual zone.
		if r.Labels[zoneLabelKey] != "" {
			c.zone = claimZone(zoneLabelKey + ": " + r.Labels[zoneLabelKey])
		}

		// Labels become diagram tags.
		c.tags = labelTags(r.Labels)

		containerByTarget[title] = c.id
		m.containers = append(m.containers, c)
	}

	needGlobal := false
	needMissing := false

	// Gears in declaration order.
	var ioGears []Gear
	for _, g := range sc.Gears {
		if g.Name == "" {
			m.problems = append(m.problems, "gear with empty name skipped")
			continue
		}
		node := &gearNode{
			id:    ids.claim(g.Name),
			name:  g.Name,
			gtype: g.Type,
			doc:   g.Doc,
			raw:   g.Raw,
		}
		// Prefer the gear's manifest (ADR 0045) for classification, doc link,
		// and I/O detection; fall back to name-prefix heuristics for a type
		// this build does not know (a cross-version or future gear).
		if man, ok := manifestFor(g.Type); ok {
			node.kind = categoryToKind(man.Category)
			node.docURL = man.DocURL()
			node.isIO = man.Terminus == sdk.TerminusIO
		} else {
			node.kind = kindForType(g.Type)
			node.docURL = docsBaseURL + "overview"
			node.isIO = node.kind == kindIO
		}
		node.tags = labelTags(g.Labels)
		if target := g.DeployTarget(); target != "" {
			if cid, known := containerByTarget[target]; known {
				node.parent = cid
			} else {
				// Deploy target not declared under racks: keep the gear
				// visible in its own implied container.
				cid := ids.claim(target)
				containerByTarget[target] = cid
				m.containers = append(m.containers, container{id: cid, title: target, desc: "Deploy target (not declared under racks)", isRack: true})
				m.problems = append(m.problems, fmt.Sprintf("gear %q deploys to undeclared target %q", g.Name, target))
				node.parent = cid
			}
		} else {
			node.parent = globalContainerID
			needGlobal = true
		}
		m.gears[g.Name] = node
		m.order = append(m.order, g.Name)

		// An I/O gear bridges one socket. Whether its far side is external is
		// decided below, once every gear's address is known.
		if node.isIO {
			ioGears = append(ioGears, g)
		}
	}

	m.resolveSockets(ioGears, ids)

	// Wires; unknown endpoints become placeholders under "unresolved".
	ensure := func(gearName string) {
		if _, ok := m.gears[gearName]; ok {
			return
		}
		node := &gearNode{
			id:      ids.claim(gearName),
			name:    gearName,
			kind:    kindMissing,
			parent:  missingContainerID,
			missing: true,
		}
		m.gears[gearName] = node
		m.order = append(m.order, gearName)
		m.problems = append(m.problems, fmt.Sprintf("wire references undefined gear %q", gearName))
		needMissing = true
	}
	for i, w := range sc.Wires {
		fg, fp, okF := splitPortRef(w.From)
		tg, tp, okT := splitPortRef(w.To)
		if !okF || !okT {
			m.problems = append(m.problems, fmt.Sprintf("wire %d has a malformed endpoint (%q -> %q)", i, w.From, w.To))
			continue
		}
		ensure(fg)
		ensure(tg)
		edge := wireEdge{fromGear: fg, fromPort: fp, toGear: tg, toPort: tp, raw: w.Raw, loop: fg == tg, tags: labelTags(w.Labels)}
		if edge.loop {
			m.gears[fg].loops = append(m.gears[fg].loops, fp+" -> "+tp)
		}
		m.wires = append(m.wires, edge)
	}

	if needGlobal {
		m.containers = append(m.containers, container{
			id:    globalContainerID,
			title: "Global gears",
			desc:  "No deploy target: runs on every connected Rack",
		})
	}
	if needMissing {
		m.containers = append(m.containers, container{
			id:    missingContainerID,
			title: "Unresolved wire endpoints",
			desc:  "Referenced by wires but not defined as gears",
		})
	}

	// Every deployment with Racks has a Mixer: each Rack connects outbound
	// to its embedded Snake, and cross-rack wires transit it.
	for _, c := range m.containers {
		if c.isRack {
			m.mixerID = ids.claim("mixer")
			break
		}
	}
	// When the scenario names the region that hosts the Mixer, nest it inside
	// that zone instead of drawing it outside every region.
	if m.mixerID != "" && sc.Meta.MixerRegion != "" {
		if id, ok := zoneByTitle[zoneLabelKey+": "+sc.Meta.MixerRegion]; ok {
			m.mixerZone = id
		} else {
			m.problems = append(m.problems, fmt.Sprintf("meta.mixer_region %q matches no rack region", sc.Meta.MixerRegion))
		}
	}
	return m
}

// labelTags renders labels as diagram tags: a marker label (value "" or
// "true") becomes just the key; otherwise key_value.
func labelTags(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tags := make([]string, 0, len(keys))
	for _, k := range keys {
		switch v := labels[k]; v {
		case "", "true":
			tags = append(tags, sanitizeID(k))
		default:
			tags = append(tags, sanitizeID(k+"_"+v))
		}
	}
	return tags
}

// peerDesc says who stands in for a counterparty, when something does. A reader
// deciding whether a suite proves anything needs to know which participants are
// real and which are simulated, and that belongs on the box rather than in
// prose somewhere else.
func peerDesc(playedBy, how string) string {
	if playedBy == "" {
		return how
	}
	return "Played by " + playedBy + ". " + how + "."
}

// resolveSockets decides, for every I/O gear, whether the far side of its
// socket is another gear in this scenario or a genuine outsider.
//
// A client whose `connect` matches a server's `bind` is talking to that server,
// and drawing both as strangers hides the one relationship a reader is looking
// for. A server keeps its external box only when nothing here dials it, because
// something still must: the traffic has to come from somewhere.
func (m *model) resolveSockets(ioGears []Gear, ids *idSet) {
	servers := map[string]string{} // normalized bind address -> gear name
	for _, g := range ioGears {
		if mode, _ := g.Config["mode"].(string); mode == "server" {
			if addr := configAddr(g.Config, "bind", "listen"); addr != "" {
				servers[normalizeAddr(addr)] = g.Name
			}
		}
	}

	dialed := map[string]bool{}
	external := make([]Gear, 0, len(ioGears))
	for _, g := range ioGears {
		mode, _ := g.Config["mode"].(string)
		if mode == "client" {
			addr := configAddr(g.Config, "connect")
			if server, ok := servers[normalizeAddr(addr)]; ok && server != g.Name {
				m.sockets = append(m.sockets, socketEdge{from: g.Name, to: server, label: socketLabel(addr)})
				dialed[server] = true
				continue
			}
		}
		external = append(external, g)
	}

	for _, g := range external {
		if dialed[g.Name] {
			continue
		}
		if ext, ok := externalFor(g, ids); ok {
			m.externals = append(m.externals, ext)
		}
	}
}

// normalizeAddr makes ":8586" and "127.0.0.1:8586" the same address, since a
// server that binds every interface is reached on the loopback like any other.
func normalizeAddr(addr string) string {
	host, port, found := strings.Cut(addr, ":")
	if !found {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "localhost", "[::]":
		host = "127.0.0.1"
	}
	return host + ":" + port
}

// externalFor derives the external socket endpoint of an I/O gear from its
// config (mode + bind/connect). Gears with no recognizable mode get none:
// the generator does not guess connection direction.
func externalFor(g Gear, ids *idSet) (extNode, bool) {
	mode, _ := g.Config["mode"].(string)

	// A counterparty the scenario names is drawn by that name. "External
	// clients (:8586)" describes a socket; "Card scheme" describes who is on
	// the other end of it, and only the second is what a reader came for.
	peer := g.Labels["peer"]
	playedBy := g.Labels["peer_played_by"]

	switch mode {
	case "server":
		addr := configAddr(g.Config, "bind", "listen")
		title := "External clients"
		if addr != "" {
			title = "External clients (" + addr + ")"
		}
		desc := "Dial " + g.Name + " over one bidirectional socket each"
		if peer != "" {
			title, desc = peer, peerDesc(playedBy, "Dials "+g.Name+" and waits for the reply on the same socket")
		}
		return extNode{
			id:      ids.claim("ext_" + g.Name),
			title:   title,
			desc:    desc,
			gear:    g.Name,
			inbound: true,
			label:   socketLabel(addr),
		}, true
	case "client":
		addr := configAddr(g.Config, "connect")
		title := "External endpoint"
		if addr != "" {
			title = "External endpoint (" + addr + ")"
		}
		desc := g.Name + " dials out over one bidirectional socket"
		if peer != "" {
			title, desc = peer, peerDesc(playedBy, g.Name+" dials it and reads the reply on the same socket")
		}
		return extNode{
			id:      ids.claim("ext_" + g.Name),
			title:   title,
			desc:    desc,
			gear:    g.Name,
			inbound: false,
			label:   socketLabel(addr),
		}, true
	default:
		return extNode{}, false
	}
}

// configAddr returns the first non-empty string among the given config keys.
func configAddr(cfg map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := cfg[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func socketLabel(addr string) string {
	if addr == "" {
		return "socket"
	}
	return "socket " + addr
}

// docsBaseURL is the public documentation site; every gear element links to
// its gear reference page (or to the catalog overview for unknown types).
const docsBaseURL = "https://fluxrig.org/docs/reference/gears/"

// categoryToKind maps a manifest gear category to a LikeC4 element kind.
func categoryToKind(c sdk.GearCategory) string {
	switch c {
	case sdk.CategoryIO:
		return kindIO
	case sdk.CategoryCodec:
		return kindCodec
	default: // logic, observability, and anything new
		return kindLogic
	}
}

// kindForType classifies a gear type into an element kind by name prefix. It
// is the fallback for a gear type this build has no manifest for; known gears
// are classified from their manifest category instead.
func kindForType(gtype string) string {
	switch {
	case strings.HasPrefix(gtype, "io_"):
		return kindIO
	case strings.HasPrefix(gtype, "codec"):
		return kindCodec
	default:
		return kindLogic
	}
}

// splitPortRef extracts (gear, port) from a wire endpoint. Endpoints are
// "gear.port" or "rack.gear.port" with dot-free segments (ADR 0043), so the last
// two segments are always the gear and its port. The optional leading rack is
// not needed to locate a gear node here because gear names are globally unique
// in a scenario today; revisit when replicated racks reuse gear names.
func splitPortRef(ref string) (gear, port string, ok bool) {
	parts := strings.Split(ref, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return "", "", false
	}
	for _, p := range parts {
		if p == "" {
			return "", "", false
		}
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

func writeSpecification(b *strings.Builder, m *model) {
	b.WriteString("// Generated by `fluxrig scenario viz`. Do not edit: regenerate from the scenario YAML.\n")
	b.WriteString("specification {\n")
	b.WriteString("  element " + kindZone + " {\n    style { color gray; opacity 5%; border dashed }\n  }\n")
	b.WriteString("  element " + kindRack + " {\n    style { color gray; opacity 10% }\n  }\n")
	b.WriteString("  element " + kindIO + " {\n    style { shape queue; color amber }\n  }\n")
	b.WriteString("  element " + kindCodec + " {\n    style { color secondary }\n  }\n")
	b.WriteString("  element " + kindLogic + " {\n    style { color primary }\n  }\n")
	b.WriteString("  element " + kindMissing + " {\n    style { color red; shape rectangle }\n  }\n")
	b.WriteString("  element " + kindExternal + " {\n    style { color slate; shape browser; opacity 40% }\n  }\n")
	// One bidirectional socket: solid, double-headed, unlike one-way wires.
	b.WriteString("  relationship socket {\n    line solid\n    head normal\n    tail normal\n  }\n")
	// Wire kinds. wire_reply (wires into a named input role, e.g. in_reply) is
	// styled apart with a dotted sky line so request and reply strands stay
	// distinguishable even where the layouter routes parallel edges on nearly
	// coincident tracks. Edge unbundling via `multiple` is intentionally omitted:
	// that relationship property postdates the pinned LikeC4 (1.50) and is a parse
	// error there, so parallel wires between the same two nodes may render bundled.
	b.WriteString("  relationship wire {\n    line solid\n  }\n")
	b.WriteString("  relationship wire_reply {\n    line dotted\n    color sky\n  }\n")
	b.WriteString("  element " + kindMixer + " {\n    style { color green }\n  }\n")
	// Persistent outbound mTLS tunnel; traffic flows both ways.
	b.WriteString("  relationship snake {\n    line solid\n    head normal\n    tail normal\n    color green\n  }\n")

	// One tag per distinct gear type plus every label/viz tag in the model.
	tags := make(map[string]bool)
	for _, name := range m.order {
		g := m.gears[name]
		if g.gtype != "" {
			tags[sanitizeID(g.gtype)] = true
		}
		for _, t := range g.tags {
			tags[t] = true
		}
	}
	for _, c := range m.containers {
		for _, t := range c.tags {
			tags[t] = true
		}
	}
	for _, w := range m.wires {
		for _, t := range w.tags {
			tags[t] = true
		}
	}
	sorted := make([]string, 0, len(tags))
	for t := range tags {
		sorted = append(sorted, t)
	}
	sort.Strings(sorted)
	for _, t := range sorted {
		fmt.Fprintf(b, "  tag %s\n", t)
	}
	b.WriteString("}\n\n")
}

// writeTags emits a tag line (tags must open an element/relationship body).
func writeTags(b *strings.Builder, indent string, tags []string) {
	if len(tags) == 0 {
		return
	}
	b.WriteString(indent)
	for i, t := range tags {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString("#" + t)
	}
	b.WriteString("\n")
}

// writeDescription emits a description property: markdown (with the raw YAML
// fenced) when a snippet is available, a plain string otherwise.
func writeDescription(b *strings.Builder, indent, text, raw string) {
	if raw != "" {
		md := text
		if md != "" {
			md += "\n\n"
		}
		if fenced := fencedYAML(raw); fenced != "" {
			md += fenced
		}
		if block, ok := markdownBlock(strings.TrimRight(md, "\n")); ok {
			fmt.Fprintf(b, "%sdescription %s\n", indent, block)
			return
		}
	}
	if text != "" {
		fmt.Fprintf(b, "%sdescription %s\n", indent, quote(text))
	}
}

// writeMixerElement emits the Mixer element at the given indent (top level, or
// nested inside its region zone).
func writeMixerElement(b *strings.Builder, m *model, indent string) {
	fmt.Fprintf(b, "%s%s = %s %s {\n%s  description %s\n%s}\n",
		indent, m.mixerID, kindMixer, quote("Mixer (control plane + Snake)"),
		indent, quote("Racks connect outbound over mTLS; scenarios, control signals and cross-rack wires transit the embedded Snake (NATS)"),
		indent)
}

func writeModel(b *strings.Builder, m *model) {
	b.WriteString("model {\n")

	writeContainer := func(c container, indent string) {
		fmt.Fprintf(b, "%s%s = %s %s {\n", indent, c.id, kindRack, quote(c.title))
		writeTags(b, indent+"  ", c.tags)
		writeDescription(b, indent+"  ", c.desc, c.raw)
		for _, name := range m.order {
			g := m.gears[name]
			if g.parent == c.id {
				writeGear(b, g, indent+"  ")
			}
		}
		fmt.Fprintf(b, "%s}\n", indent)
	}

	// Zones wrap their racks and the external endpoints of gears deployed
	// there, so a zone reads as "the region": racks plus its external world.
	for _, z := range m.zones {
		fmt.Fprintf(b, "  %s = %s %s {\n", z.id, kindZone, quote(z.title))
		for _, c := range m.containers {
			if c.zone == z.id {
				writeContainer(c, "    ")
			}
		}
		for _, e := range m.externals {
			if m.zoneOfGear(e.gear) == z.id {
				fmt.Fprintf(b, "    %s = %s %s {\n      description %s\n    }\n",
					e.id, kindExternal, quote(e.title), quote(e.desc))
			}
		}
		if m.mixerZone == z.id && m.mixerID != "" {
			writeMixerElement(b, m, "    ")
		}
		b.WriteString("  }\n")
	}
	for _, c := range m.containers {
		if c.zone == "" {
			writeContainer(c, "  ")
		}
	}
	// Gears without any container (scenario had no racks section at all and
	// every gear carries a deploy string; rare, but keep them visible).
	for _, name := range m.order {
		g := m.gears[name]
		if g.parent == "" {
			writeGear(b, g, "  ")
		}
	}

	// External socket endpoints outside every zone.
	for _, e := range m.externals {
		if m.zoneOfGear(e.gear) == "" {
			fmt.Fprintf(b, "  %s = %s %s {\n    description %s\n  }\n",
				e.id, kindExternal, quote(e.title), quote(e.desc))
		}
	}

	// The Mixer, with one Snake tunnel per Rack. It nests inside its region
	// zone when the scenario names one (meta.mixer_region); otherwise it is
	// drawn outside every zone. The Snake relationships are declared here
	// either way (they reference the Mixer by its unique id).
	if m.mixerID != "" {
		if m.mixerZone == "" {
			writeMixerElement(b, m, "  ")
		}
		for _, c := range m.containers {
			if c.isRack {
				fmt.Fprintf(b, "  %s -[snake]-> %s %s\n",
					m.containerPath(c), m.mixerID, quote("Snake (outbound mTLS)"))
			}
		}
	}

	// Wires as directed, port-labeled relationships carrying their own YAML.
	if len(m.wires) > 0 {
		b.WriteString("\n")
	}
	for _, w := range m.wires {
		if w.loop {
			// LikeC4 rejects self-relationships; loopbacks are annotated on
			// the element instead of drawn.
			continue
		}
		from := m.path(w.fromGear)
		to := m.path(w.toGear)
		label := w.fromPort + " -> " + w.toPort
		kind := "wire"
		if strings.HasPrefix(w.toPort, "in_") {
			kind = "wire_reply"
		}
		block := ""
		if fenced := fencedYAML(w.raw); fenced != "" {
			if md, ok := markdownBlock(strings.TrimRight(fenced, "\n")); ok {
				block = md
			}
		}
		if len(w.tags) == 0 && block == "" {
			fmt.Fprintf(b, "  %s -[%s]-> %s %s\n", from, kind, to, quote(label))
			continue
		}
		fmt.Fprintf(b, "  %s -[%s]-> %s %s {\n", from, kind, to, quote(label))
		writeTags(b, "    ", w.tags)
		if block != "" {
			fmt.Fprintf(b, "    description %s\n", block)
		}
		b.WriteString("  }\n")
	}
	// Sockets are bidirectional: drawn double-headed (data flows both ways);
	// the edge direction records only who establishes the connection.
	for _, sk := range m.sockets {
		fmt.Fprintf(b, "  %s -[socket]-> %s %s %s\n",
			m.path(sk.from), m.path(sk.to), quote(sk.label),
			quote("One bidirectional socket; "+sk.from+" dials "+sk.to+", data flows both ways"))
	}
	for _, e := range m.externals {
		if e.inbound {
			fmt.Fprintf(b, "  %s -[socket]-> %s %s %s\n",
				m.extPath(e), m.path(e.gear), quote(e.label),
				quote("One bidirectional socket per client; clients dial in, data flows both ways"))
		} else {
			fmt.Fprintf(b, "  %s -[socket]-> %s %s %s\n",
				m.path(e.gear), m.extPath(e), quote(e.label),
				quote("One bidirectional socket; "+e.gear+" dials out, data flows both ways"))
		}
	}
	b.WriteString("}\n\n")
}

func writeGear(b *strings.Builder, g *gearNode, indent string) {
	fmt.Fprintf(b, "%s%s = %s %s {\n", indent, g.id, g.kind, quote(g.name))
	// Tags must open the element body in the LikeC4 grammar.
	all := g.tags
	if g.gtype != "" {
		all = append([]string{sanitizeID(g.gtype)}, g.tags...)
	}
	writeTags(b, indent+"  ", all)
	if g.gtype != "" {
		fmt.Fprintf(b, "%s  technology %s\n", indent, quote(g.gtype))
		fmt.Fprintf(b, "%s  link %s %s\n", indent, g.docURL, quote(g.gtype+" gear documentation"))
	}
	desc := g.doc
	if g.missing {
		desc = "NOT DEFINED in the scenario; referenced by a wire"
	}
	for _, l := range g.loops {
		// Self-wires cannot be drawn as LikeC4 relationships; annotate instead.
		if desc != "" {
			desc += "; "
		}
		desc += "loopback wire: " + l
	}
	writeDescription(b, indent+"  ", desc, g.raw)
	fmt.Fprintf(b, "%s}\n", indent)
}

func writeViews(b *strings.Builder, m *model, help string) {
	b.WriteString("views {\n")

	fmt.Fprintf(b, "  view index {\n    title %s\n", quote("Scenario: "+m.scenarioName))
	if help != "" {
		if block, ok := markdownBlock(help); ok {
			fmt.Fprintf(b, "    description %s\n", block)
		}
	}
	// Including each zone's children renders the zone as a visual group
	// (boundary) around its racks and externals, not as a collapsed card.
	// Racks stay collapsed at this level; drill into a region view for detail.
	includes := []string{"*"}
	for _, z := range m.zones {
		includes = append(includes, z.id+".*")
	}
	fmt.Fprintf(b, "    include %s\n", strings.Join(includes, ", "))
	b.WriteString("    autoLayout LeftRight\n  }\n")

	// Drill-down per zone (regions), keeping the Snake tunnels visible.
	for _, z := range m.zones {
		zIncludes := []string{"*"}
		if m.mixerID != "" {
			zIncludes = append(zIncludes, m.mixerID)
		}
		fmt.Fprintf(b, "  view %s of %s {\n    title %s\n    include %s\n",
			viewID("of_", z.id), z.id, quote(z.title), strings.Join(zIncludes, ", "))
		b.WriteString("    autoLayout LeftRight\n  }\n")
	}

	// Drill-down per container, bringing in the external endpoints of the
	// I/O gears deployed there so the socket boundary stays visible, plus
	// the Mixer for racks (their Snake tunnel is part of their world).
	for _, c := range m.containers {
		includes := []string{"*"}
		for _, e := range m.externals {
			if m.gears[e.gear].parent == c.id {
				includes = append(includes, m.extPath(e))
			}
		}
		if c.isRack && m.mixerID != "" {
			includes = append(includes, m.mixerID)
		}
		fmt.Fprintf(b, "  view %s of %s {\n    title %s\n    include %s\n",
			viewID("of_", c.id), m.containerPath(c), quote(c.title), strings.Join(includes, ", "))
		b.WriteString("    autoLayout LeftRight\n  }\n")
	}

	// Neighborhood view per gear: the gear plus every direct wire peer. The
	// neighbor list is computed here so the DSL stays plain include-lists.
	for _, name := range m.order {
		g := m.gears[name]
		neighbors := map[string]bool{}
		for _, w := range m.wires {
			if w.fromGear == name {
				neighbors[w.toGear] = true
			}
			if w.toGear == name {
				neighbors[w.fromGear] = true
			}
		}
		paths := []string{m.path(name)}
		nSorted := make([]string, 0, len(neighbors))
		for n := range neighbors {
			nSorted = append(nSorted, n)
		}
		sort.Strings(nSorted)
		for _, n := range nSorted {
			paths = append(paths, m.path(n))
		}
		for _, e := range m.externals {
			if e.gear == name {
				paths = append(paths, m.extPath(e))
			}
		}
		fmt.Fprintf(b, "  view %s {\n    title %s\n    include %s\n",
			viewID("gear_", g.id), quote("Gear: "+g.name), strings.Join(paths, ", "))
		b.WriteString("    autoLayout LeftRight\n  }\n")
	}
	b.WriteString("}\n")
}

// containerPath returns the fully-qualified path of a container.
func (m *model) containerPath(c container) string {
	if c.zone == "" {
		return c.id
	}
	return c.zone + "." + c.id
}

// zoneOfGear returns the zone id enclosing a gear's container ("" for none).
func (m *model) zoneOfGear(gearName string) string {
	g, ok := m.gears[gearName]
	if !ok || g.parent == "" {
		return ""
	}
	for _, c := range m.containers {
		if c.id == g.parent {
			return c.zone
		}
	}
	return ""
}

// path returns the fully-qualified model path of a gear.
func (m *model) path(gearName string) string {
	g := m.gears[gearName]
	if g.parent == "" {
		return g.id
	}
	for _, c := range m.containers {
		if c.id == g.parent {
			return m.containerPath(c) + "." + g.id
		}
	}
	return g.parent + "." + g.id
}

// extPath returns the fully-qualified path of an external endpoint (nested
// in its gear's zone when there is one).
func (m *model) extPath(e extNode) string {
	if z := m.zoneOfGear(e.gear); z != "" {
		return z + "." + e.id
	}
	return e.id
}

// ---- identifiers & quoting ----

// idSet hands out unique, valid LikeC4 identifiers for scenario names (which
// allow dashes; LikeC4 identifiers do not).
type idSet struct{ used map[string]bool }

func newIDSet() *idSet { return &idSet{used: make(map[string]bool)} }

func (s *idSet) claim(name string) string {
	id := sanitizeID(name)
	if !s.used[id] {
		s.used[id] = true
		return id
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s_%d", id, i)
		if !s.used[cand] {
			s.used[cand] = true
			return cand
		}
	}
}

// sanitizeID maps an arbitrary scenario name to a LikeC4 identifier:
// [a-zA-Z_][a-zA-Z0-9_]*.
func sanitizeID(name string) string {
	var out strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	id := out.String()
	if id == "" {
		id = "unnamed"
	}
	if id[0] >= '0' && id[0] <= '9' {
		id = "n" + id
	}
	return id
}

func viewID(prefix, id string) string { return prefix + id }

// quote renders a single-quoted LikeC4 string literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}
