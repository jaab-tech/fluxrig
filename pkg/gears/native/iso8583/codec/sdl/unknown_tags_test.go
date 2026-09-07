// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Three policies for a tag nobody declared, and they have to be three different
// behaviours: the schema offered a choice the loader collapsed into two.
func TestUnknownTagPolicies(t *testing.T) {
	const head = `spec:
  id: ut
  name: ut
  version: 1.0.0
  wire:
    fields:
      0: {type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
      55:
        type: Composite
        length: 999
        prefix: ASCII.LLL
        subfields:
          layout: tlv
`
	for policy, want := range map[string]struct{ skip, store bool }{
		"preserve": {true, true},
		"drop":     {true, false},
		"reject":   {false, false},
	} {
		body := head
		if policy != "" {
			body += "          unknown_tags: " + policy + "\n"
		}
		body += `          parts:
          - {tag: "9F02", type: Binary, length: 6}
  fields:
    0: {name: MTI}
    55: {name: ICC}
`
		p := filepath.Join(t.TempDir(), "s.yaml")
		_ = os.WriteFile(p, []byte(body), 0o600)
		wire, err := WireDocument([]byte(body), filepath.Dir(p))
		if err != nil {
			t.Fatalf("%s: %v", policy, err)
		}
		got := string(wire)
		if strings.Contains(got, "skipUnknownTLVTags: true") != want.skip {
			t.Errorf("%s: skip should be %v\n%s", policy, want.skip, got)
		}
		if strings.Contains(got, "storeUnknownTLVTags: true") != want.store {
			t.Errorf("%s: store should be %v", policy, want.store)
		}
	}
}
