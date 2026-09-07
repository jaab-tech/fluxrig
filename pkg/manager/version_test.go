// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A version is a claim about content, and the CAS is where the claim is kept
// honest. Both halves of that are tested here: the declared version becomes the
// tag, and a second document claiming the same version with different bytes is
// refused.
func TestImportUsesTheVersionTheDocumentDeclares(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for name, doc := range map[string]struct{ body, wantName, wantTag string }{
		"a spec is named by its id, not its title": {
			// The title is prose and the reference spec's carries spaces, a colon
			// and parentheses — "ISO 8583:1987 (ASCII)". A reference is written as
			// name:tag, so the slug is the only half of that pair usable here.
			body:     "spec:\n  id: iso8583-v87-ascii\n  name: \"ISO 8583:1987 (ASCII)\"\n  version: 2.2.0\n  fields: {}\n",
			wantName: "iso8583-v87-ascii", wantTag: "v2.2.0",
		},
		"a spec with no id falls back to its title": {
			body:     "spec:\n  name: acme-auth\n  version: 2.2.0\n  fields: {}\n",
			wantName: "acme-auth", wantTag: "v2.2.0",
		},
		"a scenario declares under meta:": {
			body:     "meta:\n  name: payment-switch\n  version: 1.4.0\ngears: []\n",
			wantName: "payment-switch", wantTag: "v1.4.0",
		},
		"a leading v is not doubled": {
			body:     "spec:\n  name: already-v\n  version: v3.0.1\n  fields: {}\n",
			wantName: "already-v", wantTag: "v3.0.1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, t.TempDir(), "doc.yaml", doc.body)
			_, gotName, gotTag, errImport := m.Import(ctx, path, "", "")
			if errImport != nil {
				t.Fatalf("import: %v", errImport)
			}
			if gotName != doc.wantName || gotTag != doc.wantTag {
				t.Errorf("got %s:%s, want %s:%s", gotName, gotTag, doc.wantName, doc.wantTag)
			}
		})
	}
}

// Importing the same bytes twice is the ordinary case — a redeploy, a second
// rack — and it must not mint a second version of one artefact.
func TestReimportingIdenticalContentIsIdempotent(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := write(t, t.TempDir(), "spec.yaml", "spec:\n  id: acme\n  name: acme-auth\n  version: 2.2.0\n  fields: {}\n")

	h1, _, tag1, err := m.Import(ctx, path, "", "")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	h2, _, tag2, err := m.Import(ctx, path, "", "")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if tag1 != tag2 || h1 != h2 {
		t.Errorf("one file became two artefacts: %s@%.12s then %s@%.12s", tag1, h1, tag2, h2)
	}
}

// The binding a version is worth anything for: the same version cannot name two
// different contracts. This check already existed and could never fire, because
// no import ever landed on an existing tag.
func TestSameVersionWithDifferentContentIsRefused(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()

	first := write(t, dir, "a.yaml", "spec:\n  name: acme-auth\n  version: 2.2.0\n  fields: {}\n")
	if _, _, _, errFirst := m.Import(ctx, first, "", ""); errFirst != nil {
		t.Fatalf("first import: %v", errFirst)
	}

	second := write(t, dir, "b.yaml", "spec:\n  name: acme-auth\n  version: 2.2.0\n  fields: {0: {name: MTI}}\n")
	_, _, _, err = m.Import(ctx, second, "", "")
	if err == nil {
		t.Fatal("two different documents were both stored as acme-auth:v2.2.0")
	}
	if !strings.Contains(err.Error(), "different content") {
		t.Errorf("the error should say what is wrong: %v", err)
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
