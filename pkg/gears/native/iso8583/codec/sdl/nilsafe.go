// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
)

// MakeNilSafe patches a compiled spec so that every field, and every part of
// a composite, has an encoding, a prefix and a packer that tolerate what a
// spec leaves unset. A spec compiles fields that Moov cannot pack or unpack
// as they are: a base field that a spec gives positional parts keeps the
// base's type with no encoding (DE 3 of the reference v87 spec is one), and
// Moov's default packer dereferences the missing encoder. Anything that packs
// or unpacks with a compiled spec calls this first.
//
// It never assigns spec.Pad: Moov unpads on unpack whenever Pad is set, so a
// default Pad would strip leading zeros from every decoded value ("0110" to
// "110", "00" to ""). The packer pads inline instead, from the Pad the spec
// declares.
func MakeNilSafe(spec *iso8583.MessageSpec) {
	if spec == nil {
		return
	}
	safePacker := field.PackerFunc(func(data []byte, spec *field.Spec) ([]byte, error) {
		enc := spec.Enc
		if enc == nil {
			enc = encoding.ASCII
		}
		pref := spec.Pref
		if pref == nil {
			pref = prefix.ASCII.Fixed
		}
		if spec.Pad != nil {
			data = spec.Pad.Pad(data, spec.Length)
		}
		encodedValue, err := enc.Encode(data)
		if err != nil {
			return nil, fmt.Errorf("failed to encode content: %w", err)
		}
		lengthPrefix, err := pref.EncodeLength(spec.Length, len(data))
		if err != nil {
			return nil, fmt.Errorf("failed to encode length: %w", err)
		}
		return append(lengthPrefix, encodedValue...), nil
	})
	var patch func(fld field.Field)
	patch = func(fld field.Field) {
		if fld == nil {
			return
		}
		s := fld.Spec()
		if s == nil {
			return
		}
		if s.Enc == nil {
			s.Enc = encoding.ASCII
		}
		if s.Pref == nil {
			s.Pref = prefix.ASCII.Fixed
		}
		// Composite fields ignore spec.Packer (they assemble their parts
		// directly), so setting it is harmless for them.
		s.Packer = safePacker
		for _, sub := range s.Subfields {
			patch(sub)
		}
	}
	for _, fld := range spec.Fields {
		patch(fld)
	}
}
