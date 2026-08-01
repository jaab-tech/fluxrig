// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package conductor

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the Conductor gear: its port model (ADR 0043), its
// configuration schema, and its status. It is not a transparent pass-through,
// so the binding walk stops at it rather than following through.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "conductor",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Transaction switch: routes each request to a destination tree, correlates the reply under a ticket, and surfaces timeouts on the error port.",
		DocSlug:  "conductor",
		Terminus: sdk.TerminusOpaque,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "request", Summary: "Requests to route (from the local terminal path or a peer Conductor)."},
			{Name: "in_reply", Dir: sdk.PortIn, Role: "reply", Summary: "Structured replies to match to their open tickets."},
			{Name: "out_<name>", Dir: sdk.PortOut, Role: "request", Dynamic: true, Summary: "A request toward one destination (an uplink leg or a peer Conductor)."},
			{Name: "out_response", Dir: sdk.PortOut, Role: "response", Summary: "Matched, restored response toward the local origin."},
			{Name: "out_response_<origin>", Dir: sdk.PortOut, Role: "response", Dynamic: true, Summary: "Response toward the peer Conductor identified by the in-band origin stamp."},
			{Name: "error", Dir: sdk.PortOut, Role: "error", Summary: "Abnormal outcomes (no route, no destination, timeout, unmatched reply) as the original message annotated with error.reason."},
		},
		ConfigSchema: configSchemaJSON,
	}
}

// configSchemaJSON is the Conductor's configuration contract (JSON Schema
// draft-07). The destination tree is recursive and validated structurally at
// Init; here it is described loosely as a port string or a single-key strategy
// map.
const configSchemaJSON = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "origin": {
      "type": "string",
      "description": "This Conductor's name in the mesh; required only when it hands requests to, or serves requests from, other Conductors."
    },
    "correlation_key": {
      "type": "array",
      "items": { "type": "string" },
      "minItems": 1,
      "description": "Dotted-path or alias fields forming the unique per-transaction key (e.g. iso8583.field.11)."
    },
    "park_fields": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Optional fields to detach from the outbound leg and restore on the reply."
    },
    "routes": {
      "type": "array",
      "minItems": 1,
      "description": "Ordered routing table: the first route whose match predicate holds selects the destination tree. A route with no match is the catch-all.",
      "items": {
        "type": "object",
        "properties": {
          "name": { "type": "string" },
          "match": {
            "type": "object",
            "properties": {
              "field": { "type": "string" },
              "prefix": { "type": "string" }
            },
            "required": ["field"]
          },
          "destination": {
            "description": "A destination tree: an out.* port string, or a single-key map { failover|round_robin|least_loaded: [children] }."
          }
        },
        "required": ["destination"]
      }
    },
    "valet": {
      "type": "object",
      "description": "Correlation-engine (ticket store) settings: store backend and ticket lifetimes.",
      "properties": {
        "store": {
          "type": "string",
          "enum": ["memory", "local_durable", "shared"],
          "description": "Ticket store. Only 'memory' is implemented; the others are [Roadmap]."
        },
        "default_ttl": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "1m", "description": "open-ticket deadline when a request sets none." },
        "retain_after_close": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5s", "description": "window a redeemed ticket's outcome is kept for idempotent replay." }
      }
    }
  },
  "required": ["correlation_key", "routes"]
}`
