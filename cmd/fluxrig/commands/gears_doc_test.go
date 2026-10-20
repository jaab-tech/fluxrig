// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

func TestRenderManifestMarkdown(t *testing.T) {
	m := sdk.Manifest{
		Type:     "demo",
		Category: sdk.CategoryLogic,
		Status:   sdk.StatusStable,
		Terminus: sdk.TerminusOpaque,
		Summary:  "Encodes raw bytes <-> fields.",
		Ports: []sdk.Port{
			{Name: "in", Dir: sdk.PortIn, Role: "request", Summary: "Requests."},
			{Name: "out_x", Dir: sdk.PortOut, Role: "response", Dynamic: true, Summary: "Replies (a | b)."},
		},
		ConfigSchema: `{"properties":{"mode":{"type":"string","enum":["a","b"],"description":"The mode."},"n":{"type":"integer","default":7}},"required":["mode"]}`,
	}
	md := renderManifestMarkdown(m)

	for _, want := range []string{
		"<!-- AUTOGEN:manifest:demo START",
		"<!-- AUTOGEN:manifest:demo END -->",
		"| **Type** | `demo` |",
		"| `in` | input | request | Requests. |",
		"| `mode` | enum: `a`, `b` | yes | - | The mode. |",
		"| `n` | integer |  | `7` | - |",
		// MDX-hostile chars in free text are escaped (Docusaurus renders .md as MDX).
		"Encodes raw bytes &lt;-&gt; fields.",
		"| `out_x` | output (dynamic) | response | Replies (a \\| b). |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered markdown missing %q\n---\n%s", want, md)
		}
	}
	// A raw "<-" (MDX reads it as a broken JSX tag) must not survive in prose.
	if strings.Contains(md, "bytes <-") {
		t.Errorf("unescaped '<-' would break MDX:\n%s", md)
	}
	if strings.Contains(md, "—") {
		t.Errorf("rendered markdown must not use an em-dash")
	}
}

func TestInjectDocs(t *testing.T) {
	dir := t.TempDir()
	// A real registered gear so the injector finds a manifest.
	doc := "# Conductor\n\nIntro.\n\n<!-- AUTOGEN:manifest:conductor START -->\n<!-- AUTOGEN:manifest:conductor END -->\n\n## After\n"
	path := filepath.Join(dir, "conductor.md")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	f := gears.NewFactory()
	var out bytes.Buffer
	if err := injectDocs(f, dir, &out); err != nil {
		t.Fatalf("injectDocs: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "| **Type** | `conductor` |") {
		t.Fatalf("injected doc missing manifest table:\n%s", got)
	}
	if !strings.Contains(string(got), "## After") {
		t.Fatalf("injection clobbered surrounding content:\n%s", got)
	}

	// Idempotent: a second run reports no change.
	out.Reset()
	if err := injectDocs(f, dir, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 updated") {
		t.Errorf("second run not idempotent: %s", out.String())
	}
}

func TestInjectDocsUnknownType(t *testing.T) {
	dir := t.TempDir()
	doc := "<!-- AUTOGEN:manifest:nope START -->\n<!-- AUTOGEN:manifest:nope END -->\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := injectDocs(gears.NewFactory(), dir, &out); err == nil {
		t.Fatal("expected error for unknown gear type in marker")
	}
}

// A type another binary documents is left as it is, and does not fail the
// run; any other unknown type still does.
func TestInjectDocsSkipTypes(t *testing.T) {
	dir := t.TempDir()
	block := "<!-- AUTOGEN:manifest:sim_source START -->\nkept\n<!-- AUTOGEN:manifest:sim_source END -->\n"
	path := filepath.Join(dir, "sim_source.md")
	if err := os.WriteFile(path, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := injectDocs(gears.NewFactory(), dir, &out, "sim_source"); err != nil {
		t.Fatalf("a skipped type must not fail the run: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != block {
		t.Fatalf("a skipped block was rewritten:\n%s", got)
	}
	if !strings.Contains(out.String(), "left for another binary: sim_source") {
		t.Errorf("skipped block not reported: %s", out.String())
	}
	if err := injectDocs(gears.NewFactory(), dir, &out, "sim_responder"); err == nil {
		t.Fatal("a type outside the skip list must still fail")
	}
}
