// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// busNeeds lists the gears whose events this gear reads (ports.events).
func busNeeds(cfg map[string]any) []sdk.BusNeed {
	ports, _ := cfg["ports"].(map[string]any)
	entries, _ := ports["events"].([]any)
	var needs []sdk.BusNeed
	for _, entry := range entries {
		raw, _ := entry.(map[string]any)
		if ref, _ := raw["gear"].(string); ref != "" {
			needs = append(needs, sdk.BusNeed{Kind: sdk.BusReadEvents, Source: ref})
		}
	}
	return needs
}
