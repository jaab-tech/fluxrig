// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
	"github.com/moov-io/iso8583/padding"
	"github.com/moov-io/iso8583/prefix"
	"gopkg.in/yaml.v3"
)

// SDLSpec represents our YAML Spec Definition Language.
type SDLSpec struct {
	Meta struct {
		Name     string `yaml:"name"`
		Version  string `yaml:"version"`
		Protocol string `yaml:"protocol"` // e.g., "iso8583"
	} `yaml:"meta"`
	Spec struct {
	} `yaml:"spec"`
	Fields map[int]SDLField `yaml:"fields"`
}

// SDLField defines the layout and semantics of a single ISO8583 field.
type SDLField struct {
	Label   string              `yaml:"label"`
	Type    string              `yaml:"type"`
	Length  int                 `yaml:"length"`
	Alias   string              `yaml:"alias"`
	Enc     string              `yaml:"enc"`
	LenEnc  string              `yaml:"len_enc"`
	Pad     string              `yaml:"pad"`
	Storage string              `yaml:"storage"`
	Mask    bool                `yaml:"log_mask"`
	Sub     map[string]SDLField `yaml:"subfields"` // Recursive definition

	// Enhanced Structure Support
	Structure string `yaml:"structure"`        // "fixed", "tlv", "dataset"
	TLVTagEnc string `yaml:"tlv_tag_encoding"` // "hex", "int", "ascii"
	TLVLenEnc string `yaml:"tlv_len_encoding"` // "int", "bcd", "binary"
}

// FieldMeta stores pre-computed metadata for the gear to use during Process().
type FieldMeta struct {
	SpecHash   string
	Protocol   string
	Aliases    map[int]string
	SubAliases map[int]map[string]string // Field ID -> SubKey -> Alias
	IDByAlias  map[string]int
	SecureIDs  map[int]bool
}

// LoadSpec reads a YAML SDL file and returns a moov-io MessageSpec
// along with the extracted metadata.
func LoadSpec(path string) (*iso8583.MessageSpec, *FieldMeta, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read spec file: %w", err)
	}

	// 1. Compute Hash (12 hex chars)
	hash := sha256.Sum256(data)
	specHash := hex.EncodeToString(hash[:])[:12]

	// 2. Parse YAML
	var sdl SDLSpec
	if err := yaml.Unmarshal(data, &sdl); err != nil {
		return nil, nil, fmt.Errorf("failed to parse SDL: %w", err)
	}

	// 3. Build Moov MessageSpec
	moovSpec := &iso8583.MessageSpec{
		Name:   sdl.Meta.Name,
		Fields: make(map[int]field.Field),
	}

	// Default protocol if missing (backward compatibility)
	if sdl.Meta.Protocol == "" {
		sdl.Meta.Protocol = "iso8583"
	}

	// meta setup
	meta := &FieldMeta{
		SpecHash:   specHash,
		Protocol:   sdl.Meta.Protocol,
		Aliases:    make(map[int]string),
		SubAliases: make(map[int]map[string]string),
		IDByAlias:  make(map[string]int),
		SecureIDs:  make(map[int]bool),
	}

	// Iterate field IDs in order
	ids := make([]int, 0, len(sdl.Fields))
	for id := range sdl.Fields {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	for _, id := range ids {
		f := sdl.Fields[id]

		// Map moov-io field
		moovField, err := buildMoovField(id, f)
		if err != nil {
			return nil, nil, fmt.Errorf("field %d: %w", id, err)
		}
		if moovField == nil {
			return nil, nil, fmt.Errorf("unsupported field type '%s' for field %d", f.Type, id)
		}
		moovSpec.Fields[id] = moovField

		// Populate Meta
		if f.Alias != "" {
			meta.Aliases[id] = f.Alias
			meta.IDByAlias[f.Alias] = id
		}
		if f.Mask {
			meta.SecureIDs[id] = true
		}

		// Helper to collect subfield aliases
		if f.Structure != "" && len(f.Sub) > 0 {
			subMap := make(map[string]string)
			for k, sub := range f.Sub {
				if sub.Alias != "" {
					subMap[k] = sub.Alias
				}
			}
			if len(subMap) > 0 {
				meta.SubAliases[id] = subMap
			}
		}
	}

	// Ensure field 1 (Bitmap) exists if not defined, using moov-io's default if needed
	// But our SDL typically defines it.
	if _, ok := moovSpec.Fields[1]; !ok {
		// Use standard Bitmap for moov-io
		moovSpec.Fields[1] = field.NewBitmap(&field.Spec{
			Description: "Bitmap",
			Enc:         encoding.Binary, // Default for many
			Pref:        prefix.Binary.Fixed,
		})
	}

	return moovSpec, meta, nil
}

func buildMoovField(id int, f SDLField) (field.Field, error) {
	// 1. Resolve Encoders
	enc, err := resolveEncoding(f.Enc)
	if err != nil {
		return nil, err
	}
	lenEnc, _ := resolveEncoding(f.LenEnc) // lenEnc can fallback to enc
	if lenEnc == nil {
		lenEnc = enc
	}

	// 2. Create Spec
	spec := &field.Spec{
		Length:      f.Length,
		Description: f.Label,
		Enc:         enc,
		Pref:        resolvePrefix(f.Type, lenEnc),
		Pad:         resolvePadding(f.Type, f.Pad),
	}

	// 3. Create moov-io field by type
	// A structured field becomes a native moov Composite; see buildComposite.
	if f.Structure != "" {
		return buildComposite(f, spec)
	}

	var res field.Field
	switch f.Type {
	case "numeric", "n", "llvar_n", "lllvar_n":
		// We use NewString instead of NewNumeric to preserve leading zeros
		// which is critical for byte transparency in E2E round-trips.
		res = field.NewString(spec)
	case "alpha", "ans", "an", "llvar", "lllvar":
		res = field.NewString(spec)
	case "binary", "b":
		if id == 1 {
			res = field.NewBitmap(spec)
		} else {
			res = field.NewBinary(spec)
		}
	default:
		return nil, nil
	}
	return res, nil
}

func resolveEncoding(name string) (encoding.Encoder, error) {
	if name == "" {
		return nil, fmt.Errorf("missing explicit 'enc'")
	}
	switch name {
	case "ascii":
		return encoding.ASCII, nil
	case "ebcdic":
		return encoding.EBCDIC, nil
	case "bcd":
		return encoding.BCD, nil
	case "binary":
		return encoding.Binary, nil
	default:
		return encoding.ASCII, nil
	}
}

func resolvePrefix(typ string, lenEnc encoding.Encoder) prefix.Prefixer {
	// If it's a fixed length field
	switch typ {
	case "llvar", "llvar_n":
		switch lenEnc {
		case encoding.BCD:
			return prefix.BCD.LL
		case encoding.EBCDIC:
			return prefix.EBCDIC.LL
		case encoding.Binary:
			return prefix.Binary.LL
		default:
			return prefix.ASCII.LL
		}
	case "lllvar", "lllvar_n":
		switch lenEnc {
		case encoding.BCD:
			return prefix.BCD.LLL
		case encoding.Binary:
			return prefix.Binary.Fixed // 2 bytes?
		default:
			return prefix.ASCII.LLL
		}
	}

	// Fixed length
	switch lenEnc {
	case encoding.BCD:
		return prefix.BCD.Fixed
	case encoding.Binary:
		return prefix.Binary.Fixed
	case encoding.EBCDIC:
		return prefix.EBCDIC.Fixed
	default:
		return prefix.ASCII.Fixed
	}
}

func resolvePadding(typ string, pad string) padding.Padder {
	if pad == "none" {
		return nil
	}
	// Default: ISO-8583 numeric fields are often left-padded with '0'
	switch typ {
	case "numeric", "n", "llvar_n", "lllvar_n":
		return padding.Left('0')
	}
	return nil
}
