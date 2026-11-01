// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the Bento gear: a wrapper around the Bento/Benthos stream
// processor exposing its inputs/outputs and Bloblang mapping as a gear. The
// embedded Bento config is opaque to this schema (an open question for
// polymorphic configs). Named outputs are declared under config.ports.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "bento",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Wraps the Bento stream processor (file, socket, websocket and HTTP I/O, Bloblang mapping) as a gear.",
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
        "outputs": { "type": "array", "items": { "type": "string" }, "description": "Named output ports this gear emits on." },
        "events": {
          "type": "array",
          "description": "Durable events this gear reads as its input. Each entry gives one message for each event.",
          "items": {
            "type": "object",
            "properties": {
              "gear": { "type": "string", "description": "the gear whose events to read, as 'gear' (on the Rack it is deployed to, or on this Rack) or 'rack.gear'." },
              "on": { "type": "array", "items": { "type": "string", "enum": ["opened", "closed"] }, "description": "the events to read. Default: both." },
              "max_event_age": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "30s", "description": "drop an event older than this, by its time of publication." },
              "max_deliver": { "type": "integer", "minimum": 1, "default": 5, "description": "deliveries of an event whose pipeline fails." },
              "consumer_inactive_after": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "1h", "description": "delete the durable consumer of a reader that stayed away for this long." }
            },
            "required": ["gear"]
          }
        }
      }
    },
    "bento": { "type": "object", "description": "Embedded Bento config (input/pipeline/output), validated by Bento." },
    "log_level": { "type": "string", "description": "override the log level for this gear only (e.g. TRACE, DEBUG, INFO, WARN, ERROR)." }
  }
}`,
	}
}
