// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Scenario represents the static definition of the system topology.
// Reference: Topology Specification
type Scenario struct {
	Meta  ScenarioMeta `json:"meta" yaml:"meta"`
	Racks []RackTarget `json:"racks,omitempty" yaml:"racks,omitempty"`
	Gears []GearSpec   `json:"gears" yaml:"gears"`
	Wires []WireSpec   `json:"wires" yaml:"wires"`
}

type ScenarioMeta struct {
	Name    string `json:"name" yaml:"name" example:"payment-switch"`
	Version string `json:"version" yaml:"version" example:"1.0.0"`
}

// RackTarget defines where gears can run. Polymorphic (Name or Group).
type RackTarget struct {
	Name     string            `json:"name,omitempty" yaml:"name,omitempty"`
	Group    string            `json:"group,omitempty" yaml:"group,omitempty"`
	Match    *LabelMatch       `json:"match,omitempty" yaml:"match,omitempty"`
	Defaults map[string]any    `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Labels   map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Config   map[string]any    `json:"config,omitempty" yaml:"config,omitempty"`
}

type LabelMatch struct {
	Labels map[string]string `json:"labels" yaml:"labels"`
}

// GearSpec defines a logical unit of processing.
type GearSpec struct {
	ID     uuid.UUID            `json:"id,omitempty" yaml:"id,omitempty" example:"100"` // Assigned by Registry/Mixer
	Name   string               `json:"name" yaml:"name" example:"iso8583-in"`
	Type   string               `json:"type" yaml:"type" example:"iso8583-server"`
	Deploy any                  `json:"deploy" yaml:"deploy" swaggertype:"string" example:"rack-group-1"` // string (Group/Rack) or map
	Config map[string]any       `json:"config" yaml:"config" swaggertype:"object,string"`
	Ports  map[string]uuid.UUID `json:"ports,omitempty" yaml:"ports,omitempty"` // Assigned by Mixer
	Doc    string               `json:"doc,omitempty" yaml:"doc,omitempty" example:"Primary Ingress"`
}

// WireSpec defines a connection between ports.
type WireSpec struct {
	ID   uuid.UUID `json:"id,omitempty" yaml:"id,omitempty"` // Assigned by Registry/Mixer
	From string    `json:"from" yaml:"from" example:"iso8583-in.out"`
	To   string    `json:"to" yaml:"to" example:"router.in"`
}

// Validate ensures the Scenario is structurally sound.
func (s *Scenario) Validate() error {
	if s.Meta.Version == "" {
		return fmt.Errorf("scenario version is required")
	}

	// 1. Validate Racks
	rackNames := make(map[string]bool)
	groups := make(map[string]bool)

	for i, r := range s.Racks {
		if r.Name != "" {
			if !isValidName(r.Name) {
				return fmt.Errorf("invalid rack name: %s", r.Name)
			}
			if rackNames[r.Name] {
				return fmt.Errorf("duplicate rack name: %s", r.Name)
			}
			rackNames[r.Name] = true
		} else if r.Group != "" {
			if !isValidName(r.Group) {
				return fmt.Errorf("invalid group name: %s", r.Group)
			}
			if groups[r.Group] {
				return fmt.Errorf("duplicate group name: %s", r.Group)
			}
			groups[r.Group] = true
			if r.Match == nil || len(r.Match.Labels) == 0 {
				return fmt.Errorf("group %s requires match labels", r.Group)
			}
		} else if r.Defaults == nil {
			return fmt.Errorf("rack entry %d has no name, group, or defaults", i)
		}
	}

	// 2. Validate Gears
	gearNames := make(map[string]bool)
	declaredPorts := make(map[string]portDecl) // only gears that declare config.ports
	for _, g := range s.Gears {
		if g.Name == "" {
			return fmt.Errorf("gear name required")
		}
		if !isValidName(g.Name) {
			return fmt.Errorf("invalid gear name: %s", g.Name)
		}
		if gearNames[g.Name] {
			return fmt.Errorf("duplicate gear name: %s", g.Name)
		}
		gearNames[g.Name] = true

		if g.Type == "" {
			return fmt.Errorf("gear %s requires type", g.Name)
		}
		if pd, ok := portsFromConfig(g.Config); ok {
			declaredPorts[g.Name] = pd
		}

		// Deploy Target Validation
		// 'deploy' can be string (referencing rack/group) or complex.
		// For Phase 3, we support string references basically.
		if g.Deploy != nil {
			target, ok := g.Deploy.(string)
			if ok {
				if !rackNames[target] && !groups[target] {
					return fmt.Errorf("gear %s deploys to unknown target '%s'", g.Name, target)
				}
			}
		}
	}

	// 3. Validate Wires
	for i, w := range s.Wires {
		if w.From == "" || w.To == "" {
			return fmt.Errorf("wire %d missing endpoints", i)
		}
		if !isValidPortRef(w.From) {
			return fmt.Errorf("invalid wire source: %s", w.From)
		}
		if !isValidPortRef(w.To) {
			return fmt.Errorf("invalid wire target: %s", w.To)
		}

		// Fail loud on topology inconsistencies before runtime: the subscriber
		// subject is built as flux.msg.<rack>.<gear>.<port>, while a gear emits
		// on its own <rack>.<gear>.<declared-port>. A wire that names a rack or
		// gear that does not exist, or a port a gear does not declare, would
		// otherwise wire up to a subject nobody publishes to and stall silently
		// (see the bento "gear.port"-as-portname class of bug).
		fromRack, fromGear, fromPort := splitPortRef(w.From)
		toRack, toGear, toPort := splitPortRef(w.To)
		if fromRack != "" && !rackNames[fromRack] && !groups[fromRack] {
			return fmt.Errorf("wire %q: source rack %q is not defined", w.From, fromRack)
		}
		if toRack != "" && !rackNames[toRack] && !groups[toRack] {
			return fmt.Errorf("wire %q: target rack %q is not defined", w.To, toRack)
		}
		if !gearNames[fromGear] {
			return fmt.Errorf("wire %q: source gear %q is not defined", w.From, fromGear)
		}
		if !gearNames[toGear] {
			return fmt.Errorf("wire %q: target gear %q is not defined", w.To, toGear)
		}
		if pd, ok := declaredPorts[fromGear]; ok && len(pd.outputs) > 0 && !pd.outputs[fromPort] {
			return fmt.Errorf("wire %q: gear %q has no declared output port %q (declared outputs: %s)",
				w.From, fromGear, fromPort, sortedKeys(pd.outputs))
		}
		if pd, ok := declaredPorts[toGear]; ok && len(pd.inputs) > 0 && !pd.inputs[toPort] {
			return fmt.Errorf("wire %q: gear %q has no declared input port %q (declared inputs: %s)",
				w.To, toGear, toPort, sortedKeys(pd.inputs))
		}
	}

	return nil
}

// portDecl holds the input and output port names a gear explicitly declares in
// its config.ports block (e.g. the bento gear). Empty sets mean "not declared",
// in which case ports are not cross-checked (io/codec/conductor use implicit or
// dynamic ports the scenario layer cannot enumerate).
type portDecl struct {
	inputs  map[string]bool
	outputs map[string]bool
}

// portsFromConfig extracts a gear's declared ports from config.ports, tolerating
// both map[string]any and map[any]any (yaml v2/v3) shapes. It returns ok=false
// when no ports block is present.
func portsFromConfig(cfg map[string]any) (portDecl, bool) {
	raw, ok := cfg["ports"]
	if !ok {
		return portDecl{}, false
	}
	pd := portDecl{inputs: map[string]bool{}, outputs: map[string]bool{}}
	collect := func(key string, dst map[string]bool) {
		var list any
		switch m := mapValue(raw).(type) {
		case map[string]any:
			list = m[key]
		default:
			return
		}
		if items, ok := list.([]any); ok {
			for _, it := range items {
				if s, ok := it.(string); ok && s != "" {
					dst[s] = true
				}
			}
		}
	}
	collect("inputs", pd.inputs)
	collect("outputs", pd.outputs)
	if len(pd.inputs) == 0 && len(pd.outputs) == 0 {
		return portDecl{}, false
	}
	return pd, true
}

// mapValue normalizes a map[any]any to map[string]any so the ports block reads
// the same regardless of the yaml decoder in play.
func mapValue(v any) any {
	if m, ok := v.(map[any]any); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out
	}
	return v
}

// splitPortRef splits a wire endpoint into (rack, gear, port), matching the
// runtime's parsePortRef. Segments are dot-free, so the level is set by segment
// count: "gear.port" -> ("", gear, port); "rack.gear.port" -> (rack, gear, port).
func splitPortRef(ref string) (rack, gear, port string) {
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 2:
		return "", parts[0], parts[1]
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		return "", parts[0], strings.Join(parts[1:], ".")
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var nameRegex = regexp.MustCompile(`^[a-z0-9_-]+$`)

// A wire endpoint is "gear.port" or "rack.gear.port": two or three dot-free
// segments (ADR 0043). Port names may NOT contain dots — dots are pure level
// separators — so three-level addressing is unambiguous by segment count.
var portRefRegex = regexp.MustCompile(`^[a-z0-9_-]+\.[a-z0-9_-]+(\.[a-z0-9_-]+)?$`)

func isValidName(s string) bool {
	return nameRegex.MatchString(s)
}

func isValidPortRef(s string) bool {
	return portRefRegex.MatchString(s)
}
