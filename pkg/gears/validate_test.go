// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"os"
	"path/filepath"
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
	t.Run("unknown key allowed", func(t *testing.T) {
		// Extra keys (e.g. diagram labels) are permitted today.
		if err := f.ValidateConfig("io_iso8583", map[string]any{"mode": "server", "labels": map[string]any{"region": "east"}}); err != nil {
			t.Fatalf("unknown key should be allowed: %v", err)
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
