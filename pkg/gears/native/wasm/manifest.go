// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wasm

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the Wasm logic gear: runs a sandboxed WebAssembly module
// as a message transform. The module's own parameters are opaque to this
// schema (an open question for polymorphic configs).
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "wasm",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Runs a sandboxed WebAssembly module (wazero) as a polyglot message transform.",
		DocSlug:  "wasm-logic",
		Terminus: sdk.TerminusOpaque,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Message passed to the module."},
			{Name: "out", Dir: sdk.PortOut, Role: "message", Summary: "Message the module returns."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "source": { "type": "string", "description": "Location of the .wasm module: file://<path>, or snake://<bucket>/<key> for the catalog." },
    "entrypoint": { "type": "string", "description": "Exported function to call per message." },
    "memory_limit_pages": { "type": "integer", "minimum": 1, "maximum": 65536, "default": 256, "description": "Wasm linear-memory cap in 64KiB pages. A module that needs more does not load." }
  },
  "required": ["source"]
}`,
	}
}
