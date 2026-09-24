// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// deployMap returns, for every gear that names one, the Rack it is pinned to. A
// gear that names none runs on every Rack.
func deployMap(sc *registry.Scenario) map[string]string {
	m := make(map[string]string)
	for _, g := range sc.Gears {
		if target, ok := g.Deploy.(string); ok {
			m[g.Name] = target
		}
	}
	return m
}

// wireOnLane reports whether this Rack feeds the consumer of a wire from the hot
// lane. That takes a wire whose source runs on this Rack, and one that did not ask
// for the guaranteed lane: a wire from another Rack's gear can only arrive over the
// bus.
func wireOnLane(w registry.WireSpec, sourceRack, rack string) bool {
	return sourceRack == rack && w.Lane != registry.LaneGuaranteed
}

// busEmitSubjects returns the subjects a gear of this Rack emits on that must also
// be published to the bus, because something outside this Rack's memory consumes
// them: a gear on another Rack, or a wire that asked for the guaranteed lane. An
// emission on any other subject goes to the hot lane and nowhere else.
func busEmitSubjects(sc *registry.Scenario, rack string, gearDeploy map[string]string) map[string]bool {
	subjects := make(map[string]bool)
	for _, w := range sc.Wires {
		fromRack, fromGear, fromPort := parsePortRef(w.From)
		toRack, toGear, _ := parsePortRef(w.To)

		emitsHere := fromRack == rack || (fromRack == "" && (gearDeploy[fromGear] == rack || gearDeploy[fromGear] == ""))
		if !emitsHere {
			continue
		}

		// A consumer on another Rack subscribes to the subject of the Rack the source
		// is pinned to. A source that runs on every Rack has, on each, its own subject.
		// A consumer that runs on every Rack is on the others too.
		sourcePinned := fromRack != "" || gearDeploy[fromGear] != ""
		consumerElsewhere := (toRack != "" && toRack != rack) ||
			(toRack == "" && gearDeploy[toGear] != rack)

		if w.Lane == registry.LaneGuaranteed || (sourcePinned && consumerElsewhere) {
			subjects[fmt.Sprintf("flux.msg.%s.%s.%s", rack, fromGear, fromPort)] = true
		}
	}
	return subjects
}

// scenarioNeedsBus reports whether running sc on this Rack uses the bus for its
// wires: a message it publishes to it, or a consumer it feeds from it. A scenario
// whose wires all stay inside the Rack, on the hot lane, does not.
func scenarioNeedsBus(sc *registry.Scenario, rack string, gearDeploy map[string]string) bool {
	if len(busEmitSubjects(sc, rack, gearDeploy)) > 0 {
		return true
	}
	for _, w := range sc.Wires {
		fromRack, fromGear, _ := parsePortRef(w.From)
		toRack, toGear, _ := parsePortRef(w.To)

		consumedHere := (toRack == "" || toRack == rack) && (gearDeploy[toGear] == "" || gearDeploy[toGear] == rack)
		if !consumedHere {
			continue
		}
		sourceRack := firstNonEmpty(fromRack, gearDeploy[fromGear], rack)
		if !wireOnLane(w, sourceRack, rack) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// NeedsBus reports whether applying sc on this Rack uses the bus for its wires. It
// is false for a scenario whose wires all stay inside the Rack.
func (m *Manager) NeedsBus(sc *registry.Scenario) bool {
	return scenarioNeedsBus(sc, m.rackName, deployMap(sc))
}
