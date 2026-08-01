// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the TCP I/O gear. A client-mode leg owns one connection,
// so it is an I/O terminus for the binding walk.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "io_tcp",
		Category: sdk.CategoryIO,
		Status:   sdk.StatusStable,
		Summary:  "Generic TCP I/O with delimiter/length framing. Bridges one bidirectional socket onto two unidirectional ports.",
		DocSlug:  "io_tcp",
		Terminus: sdk.TerminusIO,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "egress", Summary: "Payload to frame and write to the socket."},
			{Name: "out", Dir: sdk.PortOut, Role: "ingress", Summary: "Deframed payload read from the socket."},
		},
		ConfigSchema: SchemaJSON(),
	}
}
