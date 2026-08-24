// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"fmt"
	"sort"

	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/prefix"
	moovsort "github.com/moov-io/iso8583/sort"
)

// buildComposite maps an SDL structured field onto a native moov-io Composite.
//
// Three structures are supported. "tlv" produces a tag-addressed composite, the
// EMV case: tags are read from the wire and each value carries its own length.
// "fixed" and "dataset" produce a positional composite, where subfields appear
// in a fixed order and their lengths come from their own specs.
//
// Unknown tags are preserved rather than discarded. That needs both
// SkipUnknownTLVTags and StoreUnknownTLVTags: the store logic in moov's
// composite is nested inside the skip branch, so setting only the latter
// silently does nothing and unknown tags error out instead.
func buildComposite(f SDLField, spec *field.Spec) (field.Field, error) {
	// A composite carries no encoding or padding of its own: those belong to
	// its subfields, and moov's spec validation panics if either is set. The
	// length prefix of the whole field is kept, since that is read from the
	// wire before any subfield is parsed.
	spec.Enc = nil
	spec.Pad = nil

	subKeys := make([]string, 0, len(f.Sub))
	for k := range f.Sub {
		subKeys = append(subKeys, k)
	}
	sort.Strings(subKeys)

	tlv := f.Structure == "tlv"

	subfields := make(map[string]field.Field, len(f.Sub))
	for _, k := range subKeys {
		sub, err := buildMoovField(0, f.Sub[k])
		if err != nil {
			return nil, fmt.Errorf("failed to build subfield %s: %w", k, err)
		}
		if sub == nil {
			continue
		}
		if tlv {
			// In a TLV composite the length travels in the tag header, so the
			// subfield must not also try to decode a prefix of its own.
			sub.Spec().Pref = tlvValuePrefix(f.TLVLenEnc)
		}
		subfields[k] = sub
	}
	spec.Subfields = subfields

	if !tlv {
		// Positional: subfields are packed in a deterministic order and carry
		// no tags of their own.
		spec.Tag = &field.TagSpec{Sort: moovsort.StringsByInt}
		return field.NewComposite(spec), nil
	}

	tagEnc, tagLen, tagSort, err := tlvTagSpec(f.TLVTagEnc)
	if err != nil {
		return nil, err
	}
	spec.Tag = &field.TagSpec{
		Length:              tagLen,
		Enc:                 tagEnc,
		Sort:                tagSort,
		SkipUnknownTLVTags:  true,
		StoreUnknownTLVTags: true,
		PrefUnknownTLV:      tlvValuePrefix(f.TLVLenEnc),
	}
	return field.NewComposite(spec), nil
}

// tlvTagSpec maps the SDL tag encoding onto moov's tag encoder, tag length and
// pack ordering. A zero length means the encoder determines it, which is how
// BER-TLV expresses multi-byte tags.
func tlvTagSpec(name string) (encoding.Encoder, int, moovsort.StringSlice, error) {
	switch name {
	case "hex", "":
		// BER-TLV: a tag continues into another byte when its low five bits
		// are all set, so the length cannot be fixed up front.
		return encoding.BerTLVTag, 0, moovsort.StringsByHex, nil
	case "ascii":
		return encoding.ASCII, 2, moovsort.StringsByInt, nil
	case "int":
		return encoding.BCD, 2, moovsort.StringsByInt, nil
	default:
		return nil, 0, nil, fmt.Errorf("unsupported tlv_tag_encoding %q", name)
	}
}

// tlvValuePrefix maps the SDL length encoding onto the prefixer that reads a
// TLV value length, used both for declared subfields and for unknown tags.
func tlvValuePrefix(name string) prefix.Prefixer {
	switch name {
	case "bcd":
		return prefix.BCD.LL
	case "ascii":
		return prefix.ASCII.LL
	default:
		// "binary", "int" and the empty default are BER-TLV lengths.
		return prefix.BerTLV
	}
}
