// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateConfig checks a gear instance's configuration against its manifest's
// JSON Schema (ADR 0045). It is the author-time guard the runtime applies at
// ApplyScenario so a mistyped value, a bad enum, or a missing required field
// fails loudly at activation instead of silently misbehaving at runtime.
//
// A gear type with no manifest, or a manifest with no config schema, is not
// validated (returns nil). Schemas are intentionally permissive about unknown
// keys today (they do not set additionalProperties:false), so extra keys such
// as diagram labels are allowed; type, enum, and required constraints are
// enforced.
func (f *Factory) ValidateConfig(gearType string, cfg map[string]any) error {
	man, ok := f.manifests[gearType]
	if !ok || man.ConfigSchema == "" {
		return nil
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	result, err := gojsonschema.Validate(
		gojsonschema.NewStringLoader(man.ConfigSchema),
		gojsonschema.NewGoLoader(normalizeForSchema(cfg)),
	)
	if err != nil {
		// A loader/compile error is our bug (a malformed manifest schema or a
		// config shape the loader cannot read), not the operator's.
		return fmt.Errorf("gear %q: config validation could not run: %w", gearType, err)
	}
	if result.Valid() {
		return nil
	}

	msgs := make([]string, 0, len(result.Errors()))
	for _, e := range result.Errors() {
		msgs = append(msgs, e.String())
	}
	sort.Strings(msgs)
	return fmt.Errorf("gear %q: invalid config: %s", gearType, strings.Join(msgs, "; "))
}

// normalizeForSchema converts a config tree so the JSON Schema loader can read
// it: map[any]any (which a CBOR round trip can produce) becomes
// map[string]any, recursively. Non-string map keys are rendered with %v.
func normalizeForSchema(v any) any {
	switch m := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = normalizeForSchema(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprintf("%v", k)] = normalizeForSchema(val)
		}
		return out
	case []any:
		out := make([]any, len(m))
		for i, val := range m {
			out[i] = normalizeForSchema(val)
		}
		return out
	default:
		return v
	}
}
