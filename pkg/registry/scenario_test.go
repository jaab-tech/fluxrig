// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
