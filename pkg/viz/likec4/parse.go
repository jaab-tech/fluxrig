// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package likec4

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The visualizer parses scenarios leniently, on purpose: it must render
// documents written for any fluxrig version (unknown keys, evolving gear
// schemas) because seeing a scenario is how you review one. Strict schema
// validation stays a runtime concern; the caller may surface it separately.

// Scenario is the visualizer's own lenient view of a scenario document.
// Every component keeps the raw YAML it was parsed from, so views can show
// each object next to its definition.
type Scenario struct {
	Meta  Meta
	Racks []Rack
	Gears []Gear
	Wires []Wire
}

type Meta struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	// MixerRegion, when set, names the region (rack `region` label value) that
	// hosts the Mixer, so the diagram nests it inside that zone instead of
	// drawing it outside every region.
	MixerRegion string `yaml:"mixer_region"`
}

// Labels are the single source of diagram semantics, on every component.
// On racks they are already runtime-meaningful (group matching); on gears and
// wires the runtime ignores unknown keys, so labels there are free metadata.
// The generator renders every label as a diagram tag, and the rack `region`
// label additionally groups racks into visual zones.

type Rack struct {
	Name   string            `yaml:"name"`
	Group  string            `yaml:"group"`
	Labels map[string]string `yaml:"labels"`
	Raw    string            `yaml:"-"`
}

type Gear struct {
	Name   string            `yaml:"name"`
	Type   string            `yaml:"type"`
	Deploy any               `yaml:"deploy"`
	Config map[string]any    `yaml:"config"`
	Doc    string            `yaml:"doc"`
	Labels map[string]string `yaml:"labels"`
	Raw    string            `yaml:"-"`
}

type Wire struct {
	From   string            `yaml:"from"`
	To     string            `yaml:"to"`
	Labels map[string]string `yaml:"labels"`
	Raw    string            `yaml:"-"`
}

// ParseScenario decodes a scenario document leniently: unknown keys are
// ignored, per-item decode failures become notes instead of errors, and each
// rack/gear/wire keeps its own raw YAML snippet (comments included).
func ParseScenario(content []byte) (*Scenario, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, nil, fmt.Errorf("likec4: not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("likec4: scenario must be a YAML mapping")
	}
	root := doc.Content[0]

	sc := &Scenario{}
	var problems []string

	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		switch key {
		case "meta":
			if err := val.Decode(&sc.Meta); err != nil {
				problems = append(problems, fmt.Sprintf("meta not decodable: %v", err))
			}
		case "racks":
			for _, item := range sequenceItems(val) {
				var r Rack
				if err := item.Decode(&r); err != nil {
					problems = append(problems, fmt.Sprintf("rack entry not decodable: %v", err))
					continue
				}
				r.Raw = renderNode(item)
				sc.Racks = append(sc.Racks, r)
			}
		case "gears":
			for _, item := range sequenceItems(val) {
				g, note := decodeGear(item)
				if note != "" {
					problems = append(problems, note)
				}
				if g.Name == "" {
					continue
				}
				g.Raw = renderNode(item)
				sc.Gears = append(sc.Gears, g)
			}
		case "wires":
			for _, item := range sequenceItems(val) {
				var w Wire
				if err := item.Decode(&w); err != nil {
					problems = append(problems, fmt.Sprintf("wire entry not decodable: %v", err))
					continue
				}
				w.Raw = renderNode(item)
				sc.Wires = append(sc.Wires, w)
			}
		}
	}
	return sc, problems, nil
}

// decodeGear decodes one gear item, degrading gracefully: if the full struct
// fails (e.g. a field whose schema differs across fluxrig versions), it
// falls back to extracting the identity fields one by one.
func decodeGear(item *yaml.Node) (Gear, string) {
	var g Gear
	if err := item.Decode(&g); err == nil {
		return g, ""
	}
	// Field-by-field fallback so one incompatible key cannot hide the gear.
	note := ""
	for i := 0; i+1 < len(item.Content); i += 2 {
		k, v := item.Content[i].Value, item.Content[i+1]
		switch k {
		case "name":
			g.Name = v.Value
		case "type":
			g.Type = v.Value
		case "doc":
			g.Doc = v.Value
		case "deploy":
			if v.Kind == yaml.ScalarNode {
				g.Deploy = v.Value
			}
		case "config":
			var cfg map[string]any
			if err := v.Decode(&cfg); err == nil {
				g.Config = cfg
			}
		case "labels":
			_ = v.Decode(&g.Labels)
		}
	}
	if g.Name != "" {
		note = fmt.Sprintf("gear %q decoded partially (schema mismatch on some fields)", g.Name)
	} else {
		note = "gear entry not decodable"
	}
	return g, note
}

func sequenceItems(n *yaml.Node) []*yaml.Node {
	if n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// renderNode re-encodes a YAML node as a standalone snippet (2-space indent,
// comments preserved).
func renderNode(n *yaml.Node) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return ""
	}
	_ = enc.Close()
	return strings.TrimRight(buf.String(), "\n")
}

// DeployTarget returns the gear's deploy target when it is a plain string.
func (g Gear) DeployTarget() string {
	if s, ok := g.Deploy.(string); ok {
		return s
	}
	return ""
}
