// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
