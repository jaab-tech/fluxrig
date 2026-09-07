package sdl

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v3"
)

const specDir = "../../../../../../examples/specs"

// The reference spec is the worked example of the two-layer design, and the
// schemas are what a third party validates their own spec against. Nothing bound
// the two together: when the wire keys moved out of `spec.fields`, the schema
// went on requiring them and the reference spec stopped validating against it —
// silently, because no test loaded both. This is that test.
func TestReferenceSpecValidatesAgainstItsSchema(t *testing.T) {
	doc := gojsonschema.NewGoLoader(loadSpecDocument(t, filepath.Join(specDir, "iso8583-v87-ascii.yaml")))
	for _, name := range []string{"spec.core.schema.json", "spec.fluxrig.schema.json"} {
		t.Run(name, func(t *testing.T) {
			result, err := compileSchema(t, name).Validate(doc)
			if err != nil {
				t.Fatalf("validate against %s: %v", name, err)
			}
			for _, e := range result.Errors() {
				t.Errorf("%s: %s", e.Field(), e.Description())
			}
			if !result.Valid() {
				t.Fatalf("reference spec does not validate against %s", name)
			}
		})
	}
}

// compileSchema resolves the profile's $ref to the core from disk. Both schemas
// declare absolute https $ids they are not served from, so leaving resolution to
// the validator would reach for the network and fail in CI.
func compileSchema(t *testing.T, name string) *gojsonschema.Schema {
	t.Helper()
	sl := gojsonschema.NewSchemaLoader()
	if name != "spec.core.schema.json" {
		if err := sl.AddSchemas(readSchema(t, "spec.core.schema.json")); err != nil {
			t.Fatalf("add core schema: %v", err)
		}
	}
	schema, err := sl.Compile(readSchema(t, name))
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return schema
}

func readSchema(t *testing.T, name string) gojsonschema.JSONLoader {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(specDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return gojsonschema.NewBytesLoader(raw)
}

// loadSpecDocument reads the spec and restates its keys as strings. Field ids are
// written as integers in YAML, and JSON Schema addresses object members by name,
// so a document that kept them as ints would be a document the validator cannot
// walk into.
func loadSpecDocument(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return stringKeys(doc)
}

func stringKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = stringKeys(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = stringKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stringKeys(val)
		}
		return out
	default:
		return v
	}
}
