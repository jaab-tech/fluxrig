// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateConfig checks a gear instance's configuration against its manifest's
// JSON Schema. It is the author-time guard the runtime applies at
// ApplyScenario so a mistyped value, a bad enum, or a missing required field
// fails loudly at activation instead of silently misbehaving at runtime.
//
// A gear type with no manifest, or a manifest with no config schema, is not
// validated (returns nil). A key that the schema does not declare is refused,
// at the top level and in every object the schema describes with properties:
// a misspelt option is an error and not a setting that is silently ignored (see
// strictSchema). A schema that sets additionalProperties itself keeps its
// choice, which is how a gear with a free-form config opts out. The key labels
// is always allowed at the top level, for diagram labels. Type, enum, and
// required constraints are enforced as well.
func (f *Factory) ValidateConfig(gearType string, cfg map[string]any) error {
	man, ok := f.manifests[gearType]
	if !ok || man.ConfigSchema == "" {
		return nil
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	schema, err := strictSchema(man.ConfigSchema)
	if err != nil {
		return fmt.Errorf("gear %q: config validation could not run: %w", gearType, err)
	}
	result, err := gojsonschema.Validate(
		gojsonschema.NewStringLoader(schema),
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

// strictSchema returns the schema with unknown keys refused. It sets
// additionalProperties to false on the root and on every nested object that
// declares properties, unless that object says something about
// additionalProperties itself (true opts it out). An object without properties
// is free-form and stays so. A schema that combines subschemas with properties
// (allOf, anyOf, oneOf, if, $ref, patternProperties) is left as it is, because
// closing it would refuse the keys of its other branches. The root also gets
// the key labels, which carries diagram labels and which no gear reads.
func strictSchema(schema string) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return "", err
	}
	if closeObject(doc) {
		props, _ := doc["properties"].(map[string]any)
		if _, ok := props["labels"]; !ok {
			props["labels"] = map[string]any{"type": "object"}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// closeObject refuses unknown keys in node and in the objects below it. It
// reports whether it closed node itself.
func closeObject(node map[string]any) bool {
	props, hasProps := node["properties"].(map[string]any)
	closed := false
	if hasProps && !combinesProperties(node) {
		if _, set := node["additionalProperties"]; !set {
			node["additionalProperties"] = false
			closed = true
		}
	}
	for _, p := range props {
		if sub, ok := p.(map[string]any); ok {
			closeObject(sub)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		closeObject(items)
	}
	if ap, ok := node["additionalProperties"].(map[string]any); ok {
		closeObject(ap)
	}
	return closed
}

// combinesProperties reports whether node joins subschemas that declare
// properties, in which case its own list is not the whole list of known keys.
func combinesProperties(node map[string]any) bool {
	for _, k := range []string{"if", "then", "else", "not", "$ref", "patternProperties", "dependencies"} {
		if _, ok := node[k]; ok {
			return true
		}
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := node[k].([]any)
		for _, sub := range list {
			if m, ok := sub.(map[string]any); ok {
				if _, has := m["properties"]; has {
					return true
				}
			}
		}
	}
	return false
}
