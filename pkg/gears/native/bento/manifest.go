// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the Bento gear: a wrapper around the Bento/Benthos stream
// processor exposing its inputs/outputs and Bloblang mapping as a gear. The
// embedded Bento config is opaque to this schema (ADR 0045 polymorphic-config
// open question); named outputs are declared under config.ports.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "bento",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Wraps the Bento stream processor (100+ inputs/outputs, Bloblang mapping) as a gear.",
		DocSlug:  "bento",
		Terminus: sdk.TerminusOpaque,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Message into the Bento pipeline (when an input port is declared)."},
			{Name: "out.<name>", Dir: sdk.PortOut, Role: "message", Dynamic: true, Summary: "Message out of a declared Bento output port."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "ports": {
      "type": "object",
      "description": "Declared input/output port names (bare names, e.g. 'in'/'out'); each maps to a wired flux endpoint. Omit for the implicit in/out pair.",
      "properties": {
        "inputs": { "type": "array", "items": { "type": "string" }, "description": "Named input ports this gear accepts." },
        "outputs": { "type": "array", "items": { "type": "string" }, "description": "Named output ports this gear emits on." }
      }
    },
    "bento": { "type": "object", "description": "Embedded Bento/Benthos config (input/pipeline/output), validated by Bento." },
    "log_level": { "type": "string", "description": "override the log level for this gear only (e.g. TRACE, DEBUG, INFO, WARN, ERROR)." }
  }
}`,
	}
}
