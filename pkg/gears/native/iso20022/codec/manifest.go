// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the ISO 20022 codec gear. Like codec_iso8583 it is a
// transparent single-path pass-through driven by a protocol spec; it converts
// the format and translates nothing.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "codec_iso20022",
		Category: sdk.CategoryCodec,
		Status:   sdk.StatusStable,
		Summary:  "ISO 20022 encode and decode against a protocol spec. XML becomes structured fluxMsg fields, under their element paths and aliases, and back.",
		DocSlug:  "codec-iso20022",
		Terminus: sdk.TerminusTransparent,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Message to encode or decode. Encode reads the message definition from iso20022.message when the config says auto."},
			{Name: "out", Dir: sdk.PortOut, Role: "message", Summary: "Encoded or decoded message. A decode also sets iso20022.message, iso20022.profile and the codec.spec_* keys. An encode fills the elements the profile fixes, including the message identifier."},
			{Name: "error", Dir: sdk.PortOut, Role: "error", Summary: "With on_error: reject, the message that the codec rejects, with error.reason (violation or codec_failed), error.gear and error.message. codec.violations lists the findings."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "spec_path": { "type": "string", "description": "The ISO 20022 protocol spec: a path to a file, or a store reference as name:tag (e.g. iso20022-rtp:v1.0.0) or a content hash. Its wire source fixes the profile." },
    "message": { "type": "string", "enum": ["auto", "pacs.008.001.08", "pacs.002.001.10"], "default": "auto", "description": "the message definition. auto takes it from the document that the envelope carries on decode, and from the iso20022.message metadata on encode." },
    "direction": { "type": "string", "enum": ["auto", "encode", "decode"], "default": "auto", "description": "encode, decode, or auto (decode when the message has a raw payload, encode when it does not)." },
    "participant_id": { "type": "string", "pattern": "^[A-Z0-9]{11}$", "description": "The 11-character participant identifier in the message identifier that the profile generates. Empty takes it from the iso20022.participant_id metadata, which translate writes. Set it only for an encode that no translate gear feeds." },
    "on_error": { "type": "string", "enum": ["reject", "drop", "kill"], "default": "drop", "description": "on a decode/encode failure, or on a message a rule rejects: reject (emit the message on the error port, with error.reason), drop (discard it), or kill (return the error to the runtime)." },
    "validation": { "type": "string", "enum": ["off", "warn", "enforce"], "default": "off", "description": "What the codec does with the findings of the wire validators and the spec checks. off: no checks. warn: the codec records the findings in codec.violations, and the message continues. enforce: a finding that rejects fails the message, which then follows on_error." }
  },
  "required": ["spec_path"]
}`,
	}
}
