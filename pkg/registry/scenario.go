// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"regexp"

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
			if rackNames[r.Name] {
				return fmt.Errorf("duplicate rack name: %s", r.Name)
			}
			rackNames[r.Name] = true
		} else if r.Group != "" {
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
		// TODO: Deep validation of port existence? (Requires knowing Gear Types/Schema)
		// For now, simple format check (gear.port)
		if !isValidPortRef(w.From) {
			return fmt.Errorf("invalid wire source: %s", w.From)
		}
		if !isValidPortRef(w.To) {
			return fmt.Errorf("invalid wire target: %s", w.To)
		}
	}

	return nil
}

var nameRegex = regexp.MustCompile(`^[a-z0-9_-]+$`)
var portRefRegex = regexp.MustCompile(`^[a-z0-9_-]+\.[a-z0-9_-]+$`)

func isValidName(s string) bool {
	return nameRegex.MatchString(s)
}

func isValidPortRef(s string) bool {
	return portRefRegex.MatchString(s)
}
