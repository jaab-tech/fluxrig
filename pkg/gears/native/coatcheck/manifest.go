// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// Manifest describes the Coat Check gear: sessionless request/response
// correlation via a KV-backed ticket store.
func (g *CoatCheckGear) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Type:     "coatcheck",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Summary:  "Sessionless context correlation: parks fields under a key on store, restores them on the matching reply.",
		DocSlug:  "coatcheck",
		Terminus: sdk.TerminusOpaque,
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "message", Summary: "Message to check in (store) or check out (restore)."},
			{Name: "out", Dir: sdk.PortOut, Role: "message", Summary: "Forwarded message."},
			{Name: "error", Dir: sdk.PortOut, Role: "error", Summary: "Restore misses under the configured on_missing policy."},
		},
		ConfigSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "mode": { "type": "string", "enum": ["store", "restore", "daemon"], "description": "store parks fields under the key; restore reattaches them on the matching reply; daemon governs a bucket's TTL." },
    "bucket": { "type": "string", "description": "NATS KV bucket that holds the parked context." },
    "key_fields": { "type": "array", "items": { "type": "string" }, "description": "message fields whose values form the correlation key." },
    "value_fields": { "type": "array", "items": { "type": "string" }, "description": "store mode: fields (or meta.*) to park under the key and restore later." },
    "default_ttl": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "1m", "description": "how long a parked entry lives before expiry, e.g. '30s'." },
    "max_ttl": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5m", "description": "daemon mode: safety cap on any per-message TTL override." },
    "on_missing": { "type": "string", "enum": ["error", "drop", "forward"], "description": "restore mode: what to do when no entry is found (expired/never stored): error, drop, or forward without context." },
    "merge_strategy": { "type": "string", "description": "restore mode: overwrite replaces existing metadata; any other value preserves it." },
    "storage": { "type": "string", "enum": ["file", "memory"], "description": "bucket backing store: file (durable) or memory." },
    "replicas": { "type": "integer", "description": "daemon mode: KV bucket replica count." },
    "include_values": { "type": "boolean", "default": false, "description": "daemon mode: read entry values (needed to honor per-message TTL overrides)." }
  },
  "required": ["mode", "bucket"]
}`,
	}
}
