// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the ISO 8583 I/O gear. It is the source of truth for the
// gear's identity, ports, configuration schema, and how the binding walk
// treats it (a client-mode leg owns one connection, so it is an I/O terminus).
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "io_iso8583",
		Category: sdk.CategoryIO,
		Status:   sdk.StatusStable,
		Summary:  "ISO 8583 TCP I/O: framing, TPDU, native TLS/mTLS. Bridges one bidirectional socket onto two unidirectional ports.",
		DocSlug:  "io_iso8583",
		Terminus: sdk.TerminusIO,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "egress", Summary: "Payload to frame and write to the socket (responses in server mode, requests in client mode)."},
			{Name: "out", Dir: sdk.PortOut, Role: "ingress", Summary: "Deframed payload read from the socket (requests in server mode, responses in client mode)."},
		},
		ConfigSchema: SchemaJSON(),
	}
}
