// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateConfig(t *testing.T) {
	f := NewFactory()

	t.Run("valid io_iso8583", func(t *testing.T) {
		err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "server", "bind": ":8583"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("missing required mode", func(t *testing.T) {
		if err := f.ValidateConfig("io_iso8583", map[string]any{"bind": ":8583"}); err == nil {
			t.Fatal("expected error for missing mode")
		}
	})
	t.Run("bad enum", func(t *testing.T) {
		if err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "sideways"}); err == nil {
			t.Fatal("expected error for invalid mode enum")
		}
	})
	t.Run("labels are allowed", func(t *testing.T) {
		// labels carries diagram labels. No gear reads it.
		if err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "server", "labels": map[string]any{"region": "east"}}); err != nil {
			t.Fatalf("labels should be allowed: %v", err)
		}
	})
	t.Run("unknown key refused", func(t *testing.T) {
		err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "server", "bind": ":8583", "stric_connection_routing": false})
		if err == nil || !strings.Contains(err.Error(), "stric_connection_routing") || !strings.Contains(err.Error(), "io_iso8583") {
			t.Fatalf("a misspelt key must be refused and named, got: %v", err)
		}
	})
	t.Run("unknown key in a declared nested object refused", func(t *testing.T) {
		err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "client", "connect": "h:1", "tls": map[string]any{"enabeld": true}})
		if err == nil || !strings.Contains(err.Error(), "enabeld") {
			t.Fatalf("a misspelt nested key must be refused, got: %v", err)
		}
	})
	t.Run("a declared nested key is accepted", func(t *testing.T) {
		cfg := map[string]any{
			"correlation_key": []any{"11"},
			"routes":          []any{map[string]any{"name": "a", "destination": "out_a"}},
			"valet":           map[string]any{"store": "memory", "default_ttl": "30s", "max_in_flight": 100},
		}
		if err := f.ValidateConfig("conductor", cfg); err != nil {
			t.Fatalf("valet.max_in_flight is read by the gear and must be accepted: %v", err)
		}
	})
	t.Run("an unknown key in a route is refused", func(t *testing.T) {
		cfg := map[string]any{
			"correlation_key": []any{"11"},
			"routes":          []any{map[string]any{"name": "a", "destination": "out_a", "mach": map[string]any{}}},
		}
		if err := f.ValidateConfig("conductor", cfg); err == nil || !strings.Contains(err.Error(), "mach") {
			t.Fatalf("a misspelt key in an array item must be refused, got: %v", err)
		}
	})
	t.Run("io_tcp tags are accepted", func(t *testing.T) {
		cfg := map[string]any{"mode": "server", "bind": ":3034", "tags": map[string]any{"role": "ingress"}}
		if err := f.ValidateConfig("io_tcp", cfg); err != nil {
			t.Fatalf("tags: %v", err)
		}
	})
	t.Run("no schema is not validated", func(t *testing.T) {
		if err := f.ValidateConfig("nonexistent-type", map[string]any{"anything": 1}); err != nil {
			t.Fatalf("unknown gear type must not error here: %v", err)
		}
	})
	t.Run("map[any]any normalized", func(t *testing.T) {
		cfg := map[string]any{"mode": "client", "connect": "h:1", "tls": map[any]any{"enabled": true}}
		if err := f.ValidateConfig("io_iso8583", cfg); err != nil {
			t.Fatalf("map[any]any config should validate: %v", err)
		}
	})
}

type scenarioDoc struct {
	Gears []struct {
		Name   string         `yaml:"name"`
		Type   string         `yaml:"type"`
		Config map[string]any `yaml:"config"`
	} `yaml:"gears"`
}

// TestRepoScenariosValidate is the safety net: every scenario shipped in the
// repo must pass config validation, so enabling it at ApplyScenario cannot
// silently break an existing deployment. It also keeps every gear's schema
// honest against real configs.
func TestRepoScenariosValidate(t *testing.T) {
	f := NewFactory()
	roots := []string{
		"../../examples/scenarios",
		"../../test/robot/suites",
		"../../test/e2e",
	}
	seen := 0
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var doc scenarioDoc
			if yaml.Unmarshal(data, &doc) != nil || len(doc.Gears) == 0 {
				return nil // not a scenario file
			}
			for _, g := range doc.Gears {
				if g.Type == "" {
					continue
				}
				if _, ok := f.Manifest(g.Type); !ok {
					continue // legacy/unknown type name; not this test's concern
				}
				seen++
				if err := f.ValidateConfig(g.Type, g.Config); err != nil {
					t.Errorf("%s: gear %q (%s) fails config validation: %v", path, g.Name, g.Type, err)
				}
			}
			return nil
		})
	}
	if seen == 0 {
		t.Fatal("no scenario gears were validated; sweep found nothing")
	}
	t.Logf("validated %d gear configs across repo scenarios", seen)
}

// strictSchema closes the objects that declare properties and leaves the others
// alone.
func TestStrictSchema(t *testing.T) {
	got := func(schema string) map[string]any {
		t.Helper()
		out, err := strictSchema(schema)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	t.Run("root and nested objects with properties are closed", func(t *testing.T) {
		d := got(`{"type":"object","properties":{"a":{"type":"string"},"o":{"type":"object","properties":{"x":{"type":"integer"}}}}}`)
		if d["additionalProperties"] != false {
			t.Error("root must be closed")
		}
		o := d["properties"].(map[string]any)["o"].(map[string]any)
		if o["additionalProperties"] != false {
			t.Error("a nested object with properties must be closed")
		}
		if _, ok := d["properties"].(map[string]any)["labels"]; !ok {
			t.Error("labels must be allowed on the root")
		}
	})
	t.Run("a free-form object stays free", func(t *testing.T) {
		d := got(`{"type":"object","properties":{"free":{"type":"object"}}}`)
		free := d["properties"].(map[string]any)["free"].(map[string]any)
		if _, set := free["additionalProperties"]; set {
			t.Error("an object without properties must stay free-form")
		}
	})
	t.Run("a schema that sets additionalProperties keeps its choice", func(t *testing.T) {
		d := got(`{"type":"object","additionalProperties":true,"properties":{"a":{"type":"string"}}}`)
		if d["additionalProperties"] != true {
			t.Error("additionalProperties true is the opt-out and must be kept")
		}
		if _, ok := d["properties"].(map[string]any)["labels"]; ok {
			t.Error("labels is added only when the root was closed")
		}
	})
	t.Run("items are closed", func(t *testing.T) {
		d := got(`{"type":"object","properties":{"l":{"type":"array","items":{"type":"object","properties":{"n":{"type":"string"}}}}}}`)
		items := d["properties"].(map[string]any)["l"].(map[string]any)["items"].(map[string]any)
		if items["additionalProperties"] != false {
			t.Error("the items of an array must be closed")
		}
	})
	t.Run("a schema that combines properties is left as it is", func(t *testing.T) {
		d := got(`{"type":"object","properties":{"a":{"type":"string"}},"oneOf":[{"properties":{"b":{"type":"string"}}}]}`)
		if _, set := d["additionalProperties"]; set {
			t.Error("closing it would refuse the keys of the other branches")
		}
	})
	t.Run("anyOf with only required does not stop the closing", func(t *testing.T) {
		d := got(`{"type":"object","properties":{"a":{"type":"string"}},"anyOf":[{"required":["a"]}]}`)
		if d["additionalProperties"] != false {
			t.Error("anyOf with required only must not stop the closing")
		}
	})
}

// ports.events is an array of objects, so the strict check must reach inside its
// items: a misspelt limit would otherwise stay at its default without a word.
func TestValidateConfig_BentoEvents(t *testing.T) {
	f := NewFactory()
	config := func(entry map[string]any) map[string]any {
		return map[string]any{
			"ports": map[string]any{"events": []any{entry}},
			"bento": map[string]any{},
		}
	}
	valid := map[string]any{
		"gear": "server_io", "on": []any{"opened", "closed"},
		"max_event_age": "30s", "max_deliver": 5, "consumer_inactive_after": "1h",
	}
	if err := f.ValidateConfig("bento", config(valid)); err != nil {
		t.Fatalf("a valid entry was refused: %v", err)
	}
	for name, entry := range map[string]map[string]any{
		"unknown key":   {"gear": "g", "max_age": "30s"},
		"unknown event": {"gear": "g", "on": []any{"accepted"}},
		"no gear":       {"on": []any{"opened"}},
		"bad age":       {"gear": "g", "max_event_age": "soon"},
		"zero deliver":  {"gear": "g", "max_deliver": 0},
	} {
		if err := f.ValidateConfig("bento", config(entry)); err == nil {
			t.Errorf("%s: the entry was accepted", name)
		}
	}
}
