// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScenario_Validate(t *testing.T) {
	tests := []struct {
		name    string
		s       Scenario
		wantErr string
	}{
		{
			name: "Valid Minimal",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Racks: []RackTarget{
					{Name: "rack1", Defaults: map[string]any{"a": 1}},
				},
				Gears: []GearSpec{
					{Name: "g1", Type: "t1", Deploy: "rack1"},
				},
			},
			wantErr: "",
		},
		{
			name: "Missing Version",
			s: Scenario{
				Meta: ScenarioMeta{},
			},
			wantErr: "version is required",
		},
		{
			name: "Duplicate Rack Name",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Racks: []RackTarget{
					{Name: "r1"},
					{Name: "r1"},
				},
			},
			wantErr: "duplicate rack name: r1",
		},
		{
			name: "Unknown Deploy Target",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Racks: []RackTarget{
					{Name: "rack1"},
				},
				Gears: []GearSpec{
					{Name: "g1", Type: "t1", Deploy: "ghost_rack"},
				},
			},
			wantErr: "unknown target 'ghost_rack'",
		},
		{
			name: "Invalid Wire",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Wires: []WireSpec{
					{From: "g1", To: "g2.in"}, // Missing port on From
				},
			},
			wantErr: "invalid wire source",
		},
		{
			name: "Wire To Undefined Gear",
			s: Scenario{
				Meta:  ScenarioMeta{Version: "1.0"},
				Gears: []GearSpec{{Name: "a", Type: "t"}},
				Wires: []WireSpec{
					{From: "a.out", To: "ghost.in"},
				},
			},
			wantErr: `target gear "ghost" is not defined`,
		},
		{
			name: "Wire From Undeclared Output Port",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Gears: []GearSpec{
					// Declares its output as the fully-qualified "bt.out" (the
					// bug class): the wire's resolved port is "out", which the
					// gear never publishes.
					{Name: "bt", Type: "bento", Config: map[string]any{
						"ports": map[string]any{"outputs": []any{"bt.out"}},
					}},
					{Name: "st", Type: "t"},
				},
				Wires: []WireSpec{
					{From: "bt.out", To: "st.in"},
				},
			},
			wantErr: `no declared output port "out"`,
		},
		{
			name: "Wire To Undeclared Input Port",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Gears: []GearSpec{
					{Name: "a", Type: "t"},
					{Name: "sink", Type: "bento", Config: map[string]any{
						"ports": map[string]any{"inputs": []any{"in"}},
					}},
				},
				Wires: []WireSpec{
					{From: "a.out", To: "sink.ingress"}, // "ingress" not declared
				},
			},
			wantErr: `no declared input port "ingress"`,
		},
		{
			name: "Valid Declared Ports (bare names)",
			s: Scenario{
				Meta: ScenarioMeta{Version: "1.0"},
				Gears: []GearSpec{
					{Name: "gen", Type: "bento", Config: map[string]any{
						"ports": map[string]any{"outputs": []any{"out"}},
					}},
					{Name: "sink", Type: "bento", Config: map[string]any{
						"ports": map[string]any{"inputs": []any{"in"}},
					}},
				},
				Wires: []WireSpec{
					{From: "gen.out", To: "sink.in"},
				},
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func laneScenario(lane, fromDeploy, toDeploy string) *Scenario {
	return &Scenario{
		Meta:  ScenarioMeta{Name: "lanes", Version: "1.0.0"},
		Racks: []RackTarget{{Name: "rack-a"}, {Name: "rack-b"}},
		Gears: []GearSpec{
			{Name: "src", Type: "io_tcp", Deploy: fromDeploy},
			{Name: "dst", Type: "io_tcp", Deploy: toDeploy},
		},
		Wires: []WireSpec{{From: "src.out", To: "dst.in", Lane: lane}},
	}
}

func TestScenario_WireLane(t *testing.T) {
	cases := []struct {
		name    string
		lane    string
		from    string
		to      string
		wantErr string
	}{
		{"no lane inside a rack", "", "rack-a", "rack-a", ""},
		{"no lane between racks", "", "rack-a", "rack-b", ""},
		{"hot inside a rack", LaneHot, "rack-a", "rack-a", ""},
		{"guaranteed inside a rack", LaneGuaranteed, "rack-a", "rack-a", ""},
		{"guaranteed between racks", LaneGuaranteed, "rack-a", "rack-b", ""},
		{"hot between racks is refused", LaneHot, "rack-a", "rack-b", "stays inside one Rack"},
		{"unknown lane", "warp", "rack-a", "rack-a", "unknown lane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := laneScenario(tc.lane, tc.from, tc.to).Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
