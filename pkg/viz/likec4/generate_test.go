// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package likec4

import (
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// testManifests is the manifest lookup used by the generator tests, backed by
// the real factory so classification and doc links match production.
func testManifests(gearType string) (sdk.Manifest, bool) {
	return gears.NewFactory().Manifest(gearType)
}

const multiRackYAML = `
meta:
  name: two-region-switch
  version: 1.0.0
racks:
  - name: rack-east
  - name: rack-west
gears:
  - name: terminals-east
    type: io_iso8583
    deploy: rack-east
    config: { mode: server, port: 8583 }
  - name: decode-east
    type: codec_iso8583
    deploy: rack-east
  - name: switch-east
    type: bento
    deploy: rack-east
    doc: "Routing logic"
  - name: uplink-east
    type: io_iso8583
    deploy: rack-east
    config: { mode: client }
  - name: audit
    type: bento
wires:
  - from: terminals-east.out
    to: decode-east.in
  - from: decode-east.out
    to: switch-east.in
  - from: switch-east.out
    to: uplink-east.in
  - from: switch-east.tap
    to: audit.in
  - from: switch-east.remote
    to: switch-west.in
`

func loadScenario(t *testing.T, y string) *Scenario {
	t.Helper()
	sc, notes, err := ParseScenario([]byte(y))
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if len(notes) > 0 {
		t.Fatalf("fixture parse notes: %v", notes)
	}
	return sc
}

func TestGenerateMultiRack(t *testing.T) {
	sc := loadScenario(t, multiRackYAML)
	out, problems, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, want := range []string{
		"specification {",
		"model {",
		"views {",
		// Containers from racks (dashes sanitized).
		"rack_east = rack 'rack-east'",
		"rack_west = rack 'rack-west'",
		// Gears nested with technology + tag.
		"terminals_east = io_gear 'terminals-east'",
		"technology 'io_iso8583'",
		"#io_iso8583",
		"decode_east = codec_gear 'decode-east'",
		// Every typed gear links to its public documentation page.
		"link https://fluxrig.org/docs/reference/gears/io_iso8583 'io_iso8583 gear documentation'",
		"link https://fluxrig.org/docs/reference/gears/codec-iso8583 'codec_iso8583 gear documentation'",
		"link https://fluxrig.org/docs/reference/gears/bento 'bento gear documentation'",
		"switch_east = logic_gear 'switch-east'",
		"Routing logic", // doc text leads the markdown description
		// Global gear (no deploy) under the synthetic container.
		"global_gears = rack 'Global gears'",
		// Wire relationships, port-labeled, fully qualified.
		"rack_east.terminals_east -[wire]-> rack_east.decode_east 'out -> in'",
		"rack_east.switch_east -[wire]-> global_gears.audit 'tap -> in'",
		// Undefined wire endpoint becomes a visible placeholder.
		"unresolved = rack 'Unresolved wire endpoints'",
		"switch_west = missing_gear 'switch-west'",
		"NOT DEFINED in the scenario",
		// Views: index + per-container + per-gear.
		"view index {",
		"view of_rack_east of rack_east {",
		"view gear_switch_east {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	// The undefined endpoint must be reported as a problem.
	found := false
	for _, p := range problems {
		if strings.Contains(p, "switch-west") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems missing undefined-gear note, got %v", problems)
	}
}

func TestGenerateGearNeighborhoodView(t *testing.T) {
	sc := loadScenario(t, multiRackYAML)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// switch-east's neighborhood: itself + audit, decode-east, uplink-east, switch-west.
	idx := strings.Index(out, "view gear_switch_east {")
	if idx < 0 {
		t.Fatalf("missing gear view")
	}
	section := out[idx:]
	section = section[:strings.Index(section, "}")]
	for _, want := range []string{
		"rack_east.switch_east",
		"global_gears.audit",
		"rack_east.decode_east",
		"rack_east.uplink_east",
		"unresolved.switch_west",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("gear view missing %q in %q", want, section)
		}
	}
}

func TestGenerateEdgeCases(t *testing.T) {
	t.Run("nil scenario", func(t *testing.T) {
		if _, _, err := Generate(nil, nil, testManifests); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("named port", func(t *testing.T) {
		g, p, ok := splitPortRef("conductor-east.in_reply")
		if !ok || g != "conductor-east" || p != "in_reply" {
			t.Fatalf("got %q %q %v", g, p, ok)
		}
	})

	t.Run("rack-qualified endpoint", func(t *testing.T) {
		g, p, ok := splitPortRef("worker-a.restore.out")
		if !ok || g != "restore" || p != "out" {
			t.Fatalf("got %q %q %v", g, p, ok)
		}
	})

	t.Run("malformed ref rejected", func(t *testing.T) {
		for _, ref := range []string{"", "noport", ".port", "gear."} {
			if _, _, ok := splitPortRef(ref); ok {
				t.Errorf("ref %q should be rejected", ref)
			}
		}
	})

	t.Run("id sanitization", func(t *testing.T) {
		for in, want := range map[string]string{
			"rack-east":  "rack_east",
			"3proxy":     "n3proxy",
			"a.b c":      "a_b_c",
			"":           "unnamed",
			"already_ok": "already_ok",
		} {
			if got := sanitizeID(in); got != want {
				t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("duplicate ids disambiguated", func(t *testing.T) {
		s := newIDSet()
		a := s.claim("gear-a")
		b := s.claim("gear.a") // sanitizes to the same id
		if a == b {
			t.Fatalf("collision not resolved: %q vs %q", a, b)
		}
	})

	t.Run("undeclared deploy target becomes container with note", func(t *testing.T) {
		sc := loadScenario(t, `
meta: { name: t, version: "1" }
gears:
  - name: g1
    type: bento
    deploy: ghost-rack
wires: []
`)
		out, problems, err := Generate(sc, nil, testManifests)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.Contains(out, "ghost_rack = rack 'ghost-rack'") {
			t.Errorf("undeclared target not rendered:\n%s", out)
		}
		if len(problems) == 0 || !strings.Contains(problems[0], "ghost-rack") {
			t.Errorf("expected undeclared-target problem, got %v", problems)
		}
	})

	t.Run("quote escapes", func(t *testing.T) {
		if got := quote("it's"); got != `'it\'s'` {
			t.Errorf("quote = %s", got)
		}
	})
}

func TestManifestDrivenClassification(t *testing.T) {
	// codec gear: classified and linked from its manifest, not a name table.
	sc := loadScenario(t, `
meta: { name: m, version: "1" }
racks: [ { name: r1 } ]
gears:
  - { name: dec, type: codec_iso8583, deploy: r1 }
  - { name: future, type: io_future_thing, deploy: r1 }
`)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Known gear: manifest category (codec) + manifest doc slug.
	if !strings.Contains(out, "dec = codec_gear 'dec'") {
		t.Errorf("codec not classified from manifest:\n%s", out)
	}
	if !strings.Contains(out, "link https://fluxrig.org/docs/reference/gears/codec-iso8583") {
		t.Errorf("codec doc link not from manifest:\n%s", out)
	}
	// Unknown gear: falls back to the io_ prefix heuristic + overview link.
	if !strings.Contains(out, "future = io_gear 'future'") {
		t.Errorf("unknown io_ gear should fall back to io kind:\n%s", out)
	}
	if !strings.Contains(out, "link https://fluxrig.org/docs/reference/gears/overview") {
		t.Errorf("unknown gear should link to overview:\n%s", out)
	}
}

func TestGenerateLoopback(t *testing.T) {
	sc := loadScenario(t, `
meta: { name: loopback, version: "1" }
racks: [ { name: node-01 } ]
gears:
  - { name: echo, type: io_iso8583, deploy: node-01 }
wires:
  - { from: echo.out, to: echo.in }
`)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(out, "node_01.echo -[wire]-> node_01.echo") {
		t.Errorf("self-relationship must not be emitted:\n%s", out)
	}
	if !strings.Contains(out, "loopback wire: out -> in") {
		t.Errorf("loopback annotation missing:\n%s", out)
	}
}

func TestGenerateExternalEndpoints(t *testing.T) {
	sc := loadScenario(t, `
meta: { name: perf, version: "1" }
racks: [ { name: iso-node-01 } ]
gears:
  - name: iso-gateway
    type: io_iso8583
    deploy: iso-node-01
    config: { mode: server, bind: ":8583" }
  - name: iso-client
    type: io_iso8583
    deploy: iso-node-01
    config: { mode: client, connect: "127.0.0.1:8590" }
  - name: no-mode-io
    type: io_tcp
    deploy: iso-node-01
    config: {}
wires:
  - { from: iso-gateway.out, to: iso-client.in }
  - { from: iso-client.out, to: iso-gateway.in }
`)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		// External elements, outside the rack.
		"ext_iso_gateway = external 'External clients (:8583)'",
		"ext_iso_client = external 'External endpoint (127.0.0.1:8590)'",
		// Sockets: double-headed kind; edge direction = who dials.
		"ext_iso_gateway -[socket]-> iso_node_01.iso_gateway 'socket :8583'",
		"iso_node_01.iso_client -[socket]-> ext_iso_client 'socket 127.0.0.1:8590'",
		"relationship socket {",
		"relationship wire {",
		// Rack view pulls the externals in.
		"view of_iso_node_01 of iso_node_01 {",
		"include *, ext_iso_gateway, ext_iso_client",
		// Gear neighborhood includes its external.
		"include iso_node_01.iso_gateway, iso_node_01.iso_client, ext_iso_gateway",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	// A gear with no recognizable mode gets no invented endpoint.
	if strings.Contains(out, "ext_no_mode_io") {
		t.Errorf("external invented for mode-less io gear:\n%s", out)
	}
}

func TestGenerateEmbedsSourceYAML(t *testing.T) {
	sc := loadScenario(t, multiRackYAML)
	out, _, err := Generate(sc, &Source{Name: "two_rack.yaml", YAML: []byte(multiRackYAML)}, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"description '''",
		"**Scenario** `two-region-switch` version `1.0.0` (source: `two_rack.yaml`)",
		"````yaml",
		"- from: terminals-east.out", // the raw YAML is embedded verbatim
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	// Content containing ''' falls back to the alternate delimiter.
	tricky := []byte("meta:\n  name: \"has ''' inside\"\n")
	out2, _, err := Generate(sc, &Source{Name: "t.yaml", YAML: tricky}, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out2, `description """`) {
		t.Errorf("expected fallback delimiter, got:\n%s", out2[:400])
	}
}

func TestPerComponentYAMLSnippets(t *testing.T) {
	sc := loadScenario(t, multiRackYAML)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		// The gear element carries its own YAML portion.
		"name: switch-east",
		"doc: \"Routing logic\"",
		// The rack container carries its rack entry.
		"name: rack-east",
		// A wire relationship carries its from/to portion in a body.
		"'out -> in' {",
		"from: terminals-east.out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestParseScenarioLenient(t *testing.T) {
	// Conductor-style gear schema: ports as {inputs, outputs}, named ports in
	// wires. Must parse without error (this is the cross-version case that
	// strict runtime structs reject).
	sc, notes, err := ParseScenario([]byte(`
meta: { name: lenient, version: "1" }
racks: [ { name: rack-east } ]
gears:
  - name: conductor-east
    type: conductor
    deploy: rack-east
    ports:
      inputs: ["in", "in_reply"]
      outputs: ["out_scheme_a", "out_response", "error"]
    config:
      origin: east
wires:
  - from: conductor-east.out_scheme_a
    to: encode.in
`))
	if err != nil {
		t.Fatalf("ParseScenario: %v", err)
	}
	if len(notes) > 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if len(sc.Gears) != 1 || sc.Gears[0].Name != "conductor-east" || sc.Gears[0].Type != "conductor" {
		t.Fatalf("gear not parsed: %+v", sc.Gears)
	}
	if sc.Gears[0].Config["origin"] != "east" {
		t.Fatalf("config lost: %+v", sc.Gears[0].Config)
	}
	if !strings.Contains(sc.Gears[0].Raw, "in_reply") {
		t.Fatalf("raw snippet incomplete: %s", sc.Gears[0].Raw)
	}
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, "conductor_east = logic_gear 'conductor-east'") {
		t.Errorf("conductor gear missing:\n%s", out)
	}
	if !strings.Contains(out, "'out_scheme_a -> in'") {
		t.Errorf("multi-dot port label missing:\n%s", out)
	}
}

func TestZonesAndTagsFromLabels(t *testing.T) {
	sc := loadScenario(t, `
meta: { name: zones, version: "1" }
racks:
  - name: rack-east
    labels: { region: east, tier: edge }
  - name: rack-west
    labels: { region: west }
  - name: rack-lab
gears:
  - name: gw-east
    type: io_iso8583
    deploy: rack-east
    config: { mode: server, bind: ":8583" }
    labels: { pci: "true" }
  - name: gw-west
    type: bento
    deploy: rack-west
  - name: probe
    type: bento
    deploy: rack-lab
wires:
  - from: gw-east.out
    to: gw-west.in
    labels: { cross-region: "true" }
`)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		// Zones from the region label; a rack without one stays zoneless.
		"zone_region__east = zone 'region: east'",
		"zone_region__west = zone 'region: west'",
		"rack_lab = rack 'rack-lab'",
		// Labels become tags; marker labels (value "true") use the key alone.
		"tag region_east",
		"tag tier_edge",
		"tag pci",
		"tag cross_region",
		"#region_east #tier_edge",
		// Gear label tag rides with the type tag.
		"#io_iso8583 #pci",
		// Zone-qualified paths in wires.
		"zone_region__east.rack_east.gw_east -[wire]-> zone_region__west.rack_west.gw_west 'out -> in' {",
		"#cross_region",
		// External endpoint nests inside its gear's zone.
		"zone_region__east.ext_gw_east",
		// Zone drill-down views exist; index expands zones into groups but
		// keeps racks collapsed.
		"view of_zone_region__east of zone_region__east {",
		"include *, zone_region__east.*, zone_region__west.*\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "zone 'rack-lab'") {
		t.Errorf("zoneless rack must not become a zone:\n%s", out)
	}
}

func TestMixerAndSnake(t *testing.T) {
	// Scenario with racks: Mixer present, one Snake tunnel per rack.
	sc := loadScenario(t, multiRackYAML)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"mixer = mixer 'Mixer (control plane + Snake)'",
		"relationship snake {",
		"rack_east -[snake]-> mixer 'Snake (outbound mTLS)'",
		"rack_west -[snake]-> mixer 'Snake (outbound mTLS)'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// Global-gears and unresolved containers get no Snake tunnel.
	for _, no := range []string{
		"global_gears -[snake]->",
		"unresolved -[snake]->",
	} {
		if strings.Contains(out, no) {
			t.Errorf("unexpected %q", no)
		}
	}

	// Scenario without racks (global gears only): no Mixer element.
	sc2 := loadScenario(t, `
meta: { name: g, version: "1" }
gears:
  - { name: a, type: bento }
  - { name: b, type: bento }
wires:
  - { from: a.out, to: b.in }
`)
	out2, _, err := Generate(sc2, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(out2, "= mixer ") {
		t.Errorf("mixer must not appear without racks:\n%s", out2)
	}
}

func TestMixerRegionNesting(t *testing.T) {
	base := `
meta:
  name: switch
  version: "1"
  mixer_region: "east"
racks:
  - { name: rack-east, labels: { region: east } }
  - { name: rack-west, labels: { region: west } }
gears:
  - { name: t-east, type: io_iso8583, deploy: rack-east, config: { mode: server, port: 8583 } }
  - { name: t-west, type: io_iso8583, deploy: rack-west, config: { mode: server, port: 8583 } }
wires:
  - { from: t-east.out, to: t-west.in }
`
	// With mixer_region set, the Mixer nests inside its zone (4-space indent).
	sc := loadScenario(t, base)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, "\n    mixer = mixer 'Mixer (control plane + Snake)'") {
		t.Errorf("expected Mixer nested inside its region zone (4-space indent):\n%s", out)
	}
	if !strings.Contains(out, "-[snake]-> mixer 'Snake (outbound mTLS)'") {
		t.Errorf("snake relationships to the Mixer must remain")
	}

	// An unknown region leaves the Mixer at top level (2-space) and records it.
	sc2 := loadScenario(t, strings.Replace(base, `mixer_region: "east"`, `mixer_region: "nope"`, 1))
	out2, probs, _ := Generate(sc2, nil, testManifests)
	if !strings.Contains(out2, "\n  mixer = mixer 'Mixer (control plane + Snake)'") {
		t.Errorf("unknown mixer_region should leave the Mixer at top level:\n%s", out2)
	}
	found := false
	for _, p := range probs {
		if strings.Contains(p, "mixer_region") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a problem note for an unknown mixer_region; got %v", probs)
	}
}

func TestReplyWireKind(t *testing.T) {
	sc := loadScenario(t, `
meta: { name: r, version: "1" }
racks: [ { name: r1 } ]
gears:
  - { name: cond, type: conductor, deploy: r1 }
  - { name: dec, type: codec_iso8583, deploy: r1 }
wires:
  - { from: dec.out, to: cond.in_reply }
  - { from: dec.raw, to: cond.in }
`)
	out, _, err := Generate(sc, nil, testManifests)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, "relationship wire_reply {") {
		t.Errorf("wire_reply kind missing")
	}
	if !strings.Contains(out, "r1.dec -[wire_reply]-> r1.cond 'out -> in_reply'") {
		t.Errorf("reply wire not using wire_reply kind:\n%s", out)
	}
	if !strings.Contains(out, "r1.dec -[wire]-> r1.cond 'raw -> in'") {
		t.Errorf("plain in wire must stay kind wire:\n%s", out)
	}
}
