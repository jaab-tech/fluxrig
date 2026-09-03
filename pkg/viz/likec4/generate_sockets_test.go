// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package likec4

import (
	"strings"
	"testing"
)

// TestSocketBetweenRacksIsDrawnOnce pins the behaviour a multi-rack scenario
// depends on: when one gear dials an address another gear in the same scenario
// listens on, that is one relationship, not two strangers.
func TestSocketBetweenRacksIsDrawnOnce(t *testing.T) {
	sc := &Scenario{
		Meta:  Meta{Name: "two_racks"},
		Racks: []Rack{{Name: "rack-a"}, {Name: "rack-b"}},
		Gears: []Gear{
			{Name: "caller", Type: "io_iso8583", Deploy: "rack-a",
				Config: map[string]any{"mode": "client", "connect": "127.0.0.1:9100"}},
			{Name: "listener", Type: "io_iso8583", Deploy: "rack-b",
				Config: map[string]any{"mode": "server", "bind": ":9100"}},
		},
	}

	out, _, err := Generate(sc, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if !strings.Contains(out, "rack_a.caller -[socket]-> rack_b.listener") {
		t.Errorf("the two ends must be joined; got:\n%s", out)
	}
	// The kind is always declared in the specification block; what must not
	// appear is an instance of it, which is how a counterparty inside the
	// scenario used to be drawn.
	if strings.Contains(out, "= external ") {
		t.Errorf("neither end is external: both are in the scenario; got:\n%s", out)
	}
}
