// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moov-io/iso8583/specs"
)

const refSpec = "../../../../../../examples/specs/iso8583-v87-ascii.yaml"

// The acceptance criterion for adopting Moov's wire vocabulary: removing
// fluxrig's semantic keys must leave a document Moov accepts on its own. If this
// fails, the two layers have fused again and the wire half is no longer a
// commodity we consume.
func TestStripTestYieldsAMoovDocument(t *testing.T) {
	data, err := os.ReadFile(refSpec)
	if err != nil {
		t.Fatalf("read reference spec: %v", err)
	}

	wire, err := WireDocument(data, filepath.Dir(refSpec))
	if err != nil {
		t.Fatalf("derive wire document: %v", err)
	}
	moovSpec, err := specs.ImportYAML(wire)
	if err != nil {
		t.Fatalf("moov rejected the stripped document: %v", err)
	}

	full, _, err := LoadSpec(refSpec)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if len(moovSpec.Fields) != len(full.Fields) {
		t.Errorf("stripped document has %d fields, the compiled spec %d",
			len(moovSpec.Fields), len(full.Fields))
	}
	if len(full.Fields) < 60 {
		t.Errorf("compiled %d fields, expected the full ISO 8583:87 set", len(full.Fields))
	}
}

// Stripping must remove the semantic keys and nothing else. A wire key lost here
// is a field that parses differently, which no test downstream would attribute
// to the stripper.
func TestStripKeepsWireKeysAndDropsSemanticOnes(t *testing.T) {
	in := map[string]any{
		"name": "PAN", "type": "String", "length": 19,
		"enc": "ASCII", "prefix": "ASCII.LL",
		"padding":     map[string]any{"type": "Left", "pad": "0"},
		"alias":       "card.pan",
		"sensitivity": "pan",
		"log_mask":    true,
		"scope":       "public",
		"description": "Primary account number.",
	}
	out, err := stripSemantic(in)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}

	for _, k := range []string{"type", "length", "enc", "prefix", "padding"} {
		if _, ok := out[k]; !ok {
			t.Errorf("wire key %q was dropped", k)
		}
	}
	for _, k := range []string{"alias", "sensitivity", "log_mask", "scope"} {
		if _, ok := out[k]; ok {
			t.Errorf("semantic key %q survived", k)
		}
	}
	// name is not dropped but translated: Moov calls this description.
	if out["description"] != "PAN" {
		t.Errorf("label lost in translation: got %v, want PAN", out["description"])
	}
}

// The classification is worth carrying only if it drives behaviour. A field
// marked `pan` must be treated as secure without also having to remember
// log_mask, because forgetting one of the two is how a PAN reaches a log.
func TestSensitivityImpliesSecureWithoutLogMask(t *testing.T) {
	_, meta, err := LoadSpec(refSpec)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if len(meta.SecureIDs) == 0 {
		t.Fatal("no field was classified secure")
	}
	if !meta.SecureIDs[2] {
		t.Error("DE 2 (PAN) is not secure")
	}
	if len(meta.Aliases) < 10 {
		t.Errorf("only %d aliases resolved; the semantic layer did not load", len(meta.Aliases))
	}
}

// The point of naming a base instead of restating it: the wire layer is whatever
// the linked library says it is, and only the declared differences are ours.
func TestWireBaseIsConsumedNotCopied(t *testing.T) {
	data, err := os.ReadFile(refSpec)
	if err != nil {
		t.Fatalf("read reference spec: %v", err)
	}
	if bytes.Contains(data, []byte("\n  fields:\n    0:\n      type:")) {
		t.Error("the spec restates wire attributes under spec.fields")
	}

	spec, _, err := LoadSpec(refSpec)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	base := specs.Spec87ASCII

	// Every field the base defines survives; the overlay only adds.
	for id := range base.Fields {
		if _, ok := spec.Fields[id]; !ok {
			t.Errorf("field %d present in the base is missing after the overlay", id)
		}
	}
	// And the overlay's own additions are there.
	for _, id := range []int{70, 104} {
		if _, ok := spec.Fields[id]; !ok {
			t.Errorf("field %d declared by the overlay is missing", id)
		}
	}
	if len(spec.Fields) <= len(base.Fields) {
		t.Errorf("compiled %d fields, base has %d; the overlay added nothing",
			len(spec.Fields), len(base.Fields))
	}
}

// Layer ownership is enforced, not just documented. A wire key on a semantic
// entry must fail at load rather than quietly winning over the wire document.
func TestSemanticLayerMayNotCarryWireKeys(t *testing.T) {
	doc := []byte(`
spec:
  id: t
  wire:
    source: "moov:spec87ascii"
  fields:
    2:
      alias: card.pan
      length: 19
`)
	tmp := t.TempDir() + "/spec.yaml"
	if err := os.WriteFile(tmp, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadSpec(tmp)
	if err == nil {
		t.Fatal("a wire key under spec.fields was accepted")
	}
	if !strings.Contains(err.Error(), "length") || !strings.Contains(err.Error(), "spec.wire.fields") {
		t.Errorf("error should name the key and where it belongs: %v", err)
	}
}
