// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"testing"

	"github.com/moov-io/iso8583"
)

// A tool that packs with a compiled spec, as iso8583-tool does, needs the
// spec nil-safe first: DE 3 of the reference spec has no encoding, and Moov's
// default packer dereferences it.
func TestMakeNilSafeLetsMoovPackTheReferenceSpec(t *testing.T) {
	spec, _, err := LoadSpec("../../../../../../examples/specs/iso8583-v87-ascii.yaml")
	if err != nil {
		t.Fatal(err)
	}
	MakeNilSafe(spec)
	m := iso8583.NewMessage(spec)
	for id, v := range map[int]string{0: "0200", 3: "500000", 11: "000001"} {
		if errSet := m.Field(id, v); errSet != nil {
			t.Fatalf("set DE %d: %v", id, errSet)
		}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("pack panicked: %v", r)
			}
		}()
		if _, errPack := m.Pack(); errPack != nil {
			t.Fatalf("pack: %v", errPack)
		}
	}()
}
