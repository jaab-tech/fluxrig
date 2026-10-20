// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translate

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the translate gear: it moves values from the aliases of
// one protocol spec to the aliases of another, as a translation spec says. It
// is protocol-neutral and imports no codec.
func (g *Gear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "translate",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Translates a message between two protocol specs, alias to alias, as a translation spec says. Adds the target fields and removes none.",
		DocSlug:  "translate",
		Terminus: sdk.TerminusTransparent,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Decoded message of the source spec. Its codec.spec_id and codec.spec_version must match the spec the translation names."},
			{Name: "out", Dir: sdk.PortOut, Role: "message", Summary: "The same message with the target fields added. Sets the target message identity (mti, or iso20022.message), iso20022.participant_id when the translation spec names one, and the translate.* keys. A generate binding (for example instr_id) runs here, before any conductor parks the message."},
			{Name: "error", Dir: sdk.PortOut, Role: "error", Summary: "With on_error: reject, the message that the gear rejects, with error.reason (violation or translation_failed), error.gear and error.message. translate.violations lists the findings."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "spec_path": { "type": "string", "description": "The translation spec: a path to a file, or a store reference as name:tag (e.g. billpay-rtp:v1.0.0) or a content hash. The protocol specs and the lookup sources it names resolve the same way: a file path or a store reference." },
    "translation": { "type": "string", "default": "auto", "description": "the name of one translation in the spec, or auto to choose the translation whose from message is the message that arrives." },
    "on_error": { "type": "string", "enum": ["reject", "drop", "kill"], "default": "drop", "description": "on a translation that fails, or on a finding that rejects: reject (emit the message on the error port, with error.reason), drop (discard it), or kill (return the error to the runtime)." },
    "validation": { "type": "string", "enum": ["off", "warn", "enforce"], "default": "off", "description": "What the gear does when the output misses a field that the target message requires, or when another spec version decoded the message. off: no checks. warn: the gear records the findings in translate.violations, and the message continues. enforce: a finding that rejects fails the message, which then follows on_error." }
  },
  "required": ["spec_path"]
}`,
	}
}
