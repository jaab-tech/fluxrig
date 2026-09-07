// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// A scenario names a spec, and the name has to say unambiguously whether it
// means a file or a stored artefact. Getting this wrong in either direction is
// silent: a path read as a URN fails to resolve, a URN read as a path reads the
// wrong file or none.
func TestSpecReferenceTellsAPathFromAURN(t *testing.T) {
	for ref, wantURN := range map[string]bool{
		"specs/switch_v87.yaml":  false,
		"./spec.yaml":            false,
		"spec.yml":               false,
		"/etc/fluxrig/spec.yaml": false,
		"acme-auth:v2.2.0":       true,
		"acme-auth:latest":       true,
		"1c8831d23bddaa11bb22cc33dd44ee55ff66007788990011aabbccddeeff0011": true,
	} {
		if got := isSpecURN(ref); got != wantURN {
			t.Errorf("isSpecURN(%q) = %v, want %v", ref, got, wantURN)
		}
	}
}

// The property the store buys: a reference resolves to bytes, and the same
// reference on another Rack resolves to the same bytes.
func TestSpecResolvesFromTheStore(t *testing.T) {
	dir := t.TempDir()
	mgr, err := manager.NewManager(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	body := `spec:
  id: acme-auth
  name: acme-auth
  version: 2.2.0
  wire:
    fields:
      0: {type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
      11: {type: String, length: 6, enc: ASCII, prefix: ASCII.Fixed}
  fields:
    0: {name: MTI}
    11: {name: STAN, alias: stan}
`
	src := writeFile(t, dir, "acme.yaml", body)
	_, _, tag, errImport := mgr.Import(context.Background(), src, "", "")
	if errImport != nil {
		t.Fatalf("import: %v", errImport)
	}
	if tag != "v2.2.0" {
		t.Fatalf("stored under %q, not the declared version", tag)
	}

	spec, meta, content, err := resolveSpec(context.Background(), mgr, "acme-auth:v2.2.0")
	if err != nil {
		t.Fatalf("resolve from store: %v", err)
	}
	if len(spec.Fields) != 2 {
		t.Errorf("resolved spec has %d fields, want 2", len(spec.Fields))
	}
	// The document comes back with the compiled form: anything built from the
	// spec's semantic half needs it, and a spec from the store has no path to
	// read it back from.
	if len(content) == 0 {
		t.Error("the resolved spec carries no document")
	}
	if meta.IDByAlias["stan"] != 11 {
		t.Errorf("the semantic layer did not survive the round trip: %v", meta.IDByAlias)
	}
}

// A spec that arrived as content has no directory, so a wire source naming a
// file cannot be resolved. Saying that is the whole point: resolving it against
// the process's working directory would read whatever happened to be there.
func TestStoredSpecCannotNameAFileWireSource(t *testing.T) {
	dir := t.TempDir()
	mgr, err := manager.NewManager(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	src := writeFile(t, dir, "needs-file.yaml",
		"spec:\n  id: needs-file\n  name: needs-file\n  version: 1.0.0\n  wire:\n    source: \"wire.yaml\"\n  fields:\n    0: {name: MTI}\n")
	if _, _, _, errImport := mgr.Import(context.Background(), src, "", ""); errImport != nil {
		t.Fatalf("import: %v", errImport)
	}

	_, _, _, err = resolveSpec(context.Background(), mgr, "needs-file:v1.0.0")
	if err == nil {
		t.Fatal("a file wire source resolved with no directory to resolve against")
	}
	if !strings.Contains(err.Error(), "wire.fields") {
		t.Errorf("the error should say what to do instead: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
