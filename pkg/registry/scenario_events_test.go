// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import "testing"

func eventScenario(ref string) *Scenario {
	return &Scenario{
		Meta: ScenarioMeta{Name: "events", Version: "1.0.0"},
		Gears: []GearSpec{
			{Name: "server_io", Type: "io_iso8583"},
			{Name: "on_connect", Type: "bento", Config: map[string]any{
				"ports": map[string]any{"events": []any{map[string]any{"gear": ref}}},
			}},
		},
	}
}

func TestValidate_EventSourceMustBeAGearOfTheScenario(t *testing.T) {
	for ref, wantErr := range map[string]bool{
		"server_io":       false,
		"rack1.server_io": false,
		"sever_io":        true,
		"rack1.sever_io":  true,
	} {
		err := eventScenario(ref).Validate()
		if (err != nil) != wantErr {
			t.Errorf("ref %q: Validate error = %v, wantErr %t", ref, err, wantErr)
		}
	}
}
