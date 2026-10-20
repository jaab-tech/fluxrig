// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// The configuration keys the gear documents. A key added to the schema
// without a documented meaning, or documented and missing here, fails.
var documentedKeys = []string{"spec_path", "translation", "on_error", "validation"}

func TestManifestDeclaresTheDocumentedConfig(t *testing.T) {
	m := (&Gear{}).Manifest()
	if m.Type != "translate" {
		t.Fatalf("Type = %q, want %q", m.Type, "translate")
	}
	if m.Status != sdk.StatusStable {
		t.Fatalf("Status = %q, want %q", m.Status, sdk.StatusStable)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(m.ConfigSchema), &schema); err != nil {
		t.Fatalf("config schema is not valid JSON: %v", err)
	}
	var got []string
	for k := range schema.Properties {
		got = append(got, k)
	}
	slices.Sort(got)
	want := slices.Clone(documentedKeys)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("schema keys = %v, want %v", got, want)
	}
	if !slices.Equal(schema.Required, []string{"spec_path"}) {
		t.Fatalf("required = %v, want [spec_path]", schema.Required)
	}
}
