// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the ISO 8583 codec gear. It is a transparent single-path
// pass-through, so the binding walk follows it through to the next gear.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "codec_iso8583",
		Category: sdk.CategoryCodec,
		Status:   sdk.StatusStable,
		Summary:  "ISO 8583 encode/decode against an SDL spec: raw wire bytes <-> structured fluxMsg fields.",
		DocSlug:  "codec-iso8583",
		Terminus: sdk.TerminusTransparent,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Message to encode or decode."},
			{Name: "out", Dir: sdk.PortOut, Role: "message", Summary: "Encoded or decoded message. A decode also sets iso8583.mti and iso8583.mti_class, the latter being the leading digits a request and its reply share."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "spec_path": { "type": "string", "description": "Path or URN of the ISO 8583 SDL spec." },
    "spec": { "type": "string", "description": "Legacy alias of spec_path." },
    "direction": { "type": "string", "enum": ["auto", "encode", "decode"], "default": "auto", "description": "encode, decode, or auto (infer from the message)." },
    "on_error": { "type": "string", "enum": ["reject", "drop", "kill"], "default": "reject", "description": "on a decode/encode failure: reject (emit on the error path), drop (discard), or kill (fail the gear)." }
  },
  "anyOf": [
    { "required": ["spec_path"] },
    { "required": ["spec"] }
  ]
}`,
	}
}
