// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// GetValue extracts a value from FluxMsg using a dot-notation path.
// Supports: "data.field", "meta.header", "flux_id", "trace_id", "src_id".
func GetValue(msg *fluxmsg.FluxMsg, path string) (any, bool) {
	if path == "payload" {
		return string(msg.RawPayload), true
	}
	if path == "flux_id" {
		return msg.FluxID, true
	}
	if path == "trace_id" {
		return msg.TraceID, true
	}
	if path == "src_id" {
		return msg.SrcGearID, true
	}

	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil, false
	}

	root := parts[0]
	rest := parts[1:]

	if root == "meta" {
		// Meta is flat string map, but keys might contain dots (namespaced)
		// e.g. "meta.iso8583.raw_header" -> "iso8583.raw_header"
		key := strings.Join(rest, ".")
		val, ok := msg.Metadata[key]
		return val, ok
	}

	if root == "data" {
		// Data is map[string]any
		current := msg.Data
		for i, part := range rest {
			val, ok := current[part]
			if !ok {
				return nil, false
			}
			if i == len(rest)-1 {
				return val, true
			}
			// Descent
			if next, ok := val.(map[string]any); ok {
				current = next
			} else {
				// Path mismatch (not a map)
				return nil, false
			}
		}
	}

	return nil, false
}

// JoinKeys Helper to create composite keys (concat with underscores).
func JoinKeys(parts ...string) string {
	return strings.Join(parts, "_")
}
