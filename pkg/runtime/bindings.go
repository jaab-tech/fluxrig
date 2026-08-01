// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

const maxWalkDepth = 32 // cycle/misconfiguration guard

// computeBindings resolves, for every gear deployed on this rack, the terminus
// of each of its named output ports by walking the wire graph downstream:
// through transparent single-path gears (codecs) until a local I/O gear, the
// rack boundary, or ambiguity. The result is static per activation and handed
// to gears through their context, so sensing always matches the live wiring
// and topology is never duplicated in gear configuration.
// terminusOf reports how the binding walk should treat a gear type when it is
// reached as a wire endpoint, from that gear's manifest (ADR 0045). It
// replaces the hard-coded io-gear-type set, so a new I/O gear is classified by
// its manifest, not by a table the runtime must remember to update.
func computeBindings(sc *registry.Scenario, rackName string, gearDeploy map[string]string, terminusOf func(gearType string) sdk.TerminusKind) map[string]map[string]sdk.PortBinding {
	specs := make(map[string]*registry.GearSpec, len(sc.Gears))
	for i := range sc.Gears {
		specs[sc.Gears[i].Name] = &sc.Gears[i]
	}

	// Outgoing wires grouped by their exact source reference "gear.port".
	wiresFrom := make(map[string][]string, len(sc.Wires))
	for _, w := range sc.Wires {
		wiresFrom[w.From] = append(wiresFrom[w.From], w.To)
	}

	rackOf := func(gear string) string {
		if r, ok := gearDeploy[gear]; ok && r != "" {
			return r
		}
		return rackName // global gears run locally
	}

	// walk resolves one wire endpoint to a terminus.
	var walk func(toRef string, depth int) sdk.PortBinding
	walk = func(toRef string, depth int) sdk.PortBinding {
		if depth > maxWalkDepth {
			return sdk.PortBinding{Kind: sdk.BindingUnbound}
		}
		_, gear, _ := parsePortRef(toRef)
		spec, ok := specs[gear]
		if !ok {
			return sdk.PortBinding{Kind: sdk.BindingUnbound}
		}
		if rackOf(gear) != rackName {
			// Crossing the rack boundary is precisely what makes a
			// destination remote.
			return sdk.PortBinding{Kind: sdk.BindingRemote, Gear: gear}
		}
		switch terminusOf(spec.Type) {
		case sdk.TerminusIO:
			// An I/O gear owns one connection: its link-state is the signal.
			return sdk.PortBinding{Kind: sdk.BindingIO, Gear: gear}
		case sdk.TerminusOpaque:
			// A leg that ends here but is not a watchable socket.
			return sdk.PortBinding{Kind: sdk.BindingUnbound}
		default:
			// Transparent pass-through (codec) or an unknown terminus: follow
			// the gear's single default output.
			outs := wiresFrom[gear+".out"]
			if len(outs) != 1 {
				return sdk.PortBinding{Kind: sdk.BindingUnbound}
			}
			return walk(outs[0], depth+1)
		}
	}

	out := make(map[string]map[string]sdk.PortBinding)
	for _, w := range sc.Wires {
		_, fromGear, fromPort := parsePortRef(w.From)
		if fromPort == "" || !strings.HasPrefix(fromPort, "out") {
			continue // bindings describe named output ports only
		}
		if rackOf(fromGear) != rackName {
			continue // only gears hosted here receive bindings
		}
		if out[fromGear] == nil {
			out[fromGear] = make(map[string]sdk.PortBinding)
		}
		if _, done := out[fromGear][fromPort]; done {
			// A port wired to several inputs broadcasts; sensing cannot bind
			// to a single terminus.
			out[fromGear][fromPort] = sdk.PortBinding{Kind: sdk.BindingUnbound}
			continue
		}
		out[fromGear][fromPort] = walk(w.To, 0)
	}
	return out
}
