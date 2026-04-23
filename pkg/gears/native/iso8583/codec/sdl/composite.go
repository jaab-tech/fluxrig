// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/moov-io/iso8583/encoding"
	"github.com/moov-io/iso8583/field"
)

// CompositeConfig holds the parsing rules for the composite field.
type CompositeConfig struct {
	Structure string // "fixed", "tlv", "dataset"
	TagEnc    string // "hex", "ascii" for TLV tags
	LenEnc    string // "int", "bcd" for TLV lengths
}

// CompositeField handles subfields (Fixed, TLV).
type CompositeField struct {
	spec      *field.Spec
	config    CompositeConfig
	subfields map[string]field.Field // Keyed by "ID" or "Tag"
	ordered   []string               // Order of subfields (critical for Fixed/Dataset)
	values    map[string]string      // Parsed values
	data      []byte                 // Raw data
}

func NewCompositeField(spec *field.Spec, cfg CompositeConfig) *CompositeField {
	return &CompositeField{
		spec:      spec,
		config:    cfg,
		subfields: make(map[string]field.Field),
		values:    make(map[string]string),
	}
}

func (c *CompositeField) AddSubfield(key string, f field.Field) {
	c.subfields[key] = f
	c.ordered = append(c.ordered, key)
}

func (c *CompositeField) GetSubvalues() map[string]string {
	return c.values
}

// moov-io field.Field interface implementation
func (c *CompositeField) Spec() *field.Spec        { return c.spec }
func (c *CompositeField) SetSpec(spec *field.Spec) { c.spec = spec }
func (c *CompositeField) SetBytes(b []byte) error {
	c.data = b
	c.syncState()
	return nil
}
func (c *CompositeField) Bytes() ([]byte, error)  { return c.data, nil }
func (c *CompositeField) String() (string, error) { return string(c.data), nil }

func (c *CompositeField) Pack() ([]byte, error) {
	var content bytes.Buffer

	switch c.config.Structure {
	case "tlv":
		for _, tag := range c.ordered {
			f := c.subfields[tag]
			c.hardenField(f)
			if val, ok := c.values[tag]; ok {
				if err := f.Marshal(val); err != nil {
					return nil, fmt.Errorf("failed to marshal subfield %s: %w", tag, err)
				}
			}
			b, err := f.Pack()
			if err != nil {
				return nil, fmt.Errorf("failed to pack subfield %s: %w", tag, err)
			}

			// 1. Pack Tag
			tagBytes, err := c.encodeTag(tag)
			if err != nil {
				return nil, err
			}
			content.Write(tagBytes)

			// 2. Pack Length
			// Note: The length is usually the length of the PACKED field content
			lenBytes, err := c.encodeTLVLength(len(b))
			if err != nil {
				return nil, err
			}
			content.Write(lenBytes)

			// 3. Pack Value
			content.Write(b)
		}

	case "fixed", "": // Default to positional
		for _, key := range c.ordered {
			f, ok := c.subfields[key]
			if !ok {
				continue
			}
			c.hardenField(f)
			if val, ok := c.values[key]; ok {
				if err := f.Marshal(val); err != nil {
					return nil, fmt.Errorf("failed to marshal subfield %s: %w", key, err)
				}
			}
			b, err := f.Pack()
			if err != nil {
				return nil, fmt.Errorf("failed to pack subfield %s: %w", key, err)
			}
			content.Write(b)
		}
	}

	res := content.Bytes()

	// Wrap with parent length prefix if spec defined
	if c.spec != nil && c.spec.Pref != nil {
		pref, err := c.spec.Pref.EncodeLength(c.spec.Length, len(res))
		if err != nil {
			return nil, fmt.Errorf("failed to encode length prefix: %w", err)
		}
		return append(pref, res...), nil
	}

	return res, nil
}

func (c *CompositeField) Unpack(data []byte) (int, error) {
	// 1. Decode Length Prefix
	payloadLen, prefixLen, err := c.decodeFieldLength(c, data)
	if err != nil {
		return 0, fmt.Errorf("failed to decode length prefix: %w", err)
	}

	totalLen := prefixLen + payloadLen
	if len(data) < totalLen {
		return 0, fmt.Errorf("not enough data: need %d, have %d", totalLen, len(data))
	}

	c.data = data[:totalLen]
	content := data[prefixLen : prefixLen+payloadLen]

	// 2. Parse Subfields
	if len(c.subfields) > 0 {
		if err := c.unpackSubfields(content); err != nil {
			return 0, err
		}
	}

	return totalLen, nil
}

// Unmarshal allows extracting the field value into a Go type.
func (c *CompositeField) Unmarshal(v any) error {
	// For now, support string or *string
	switch val := v.(type) {
	case *string:
		*val = string(c.data)
	case *[]byte:
		*val = c.data
	default:
		return fmt.Errorf("unsupported type for CompositeField.Unmarshal: %T", v)
	}
	return nil
}

// Marshal ensures the field implements field.Field interface (likely for setting value from struct).
func (c *CompositeField) Marshal(v any) error {
	switch val := v.(type) {
	case string:
		return c.SetData(val)
	case []byte:
		return c.SetBytes(val)
	case nil:
		return nil
	default:
		return fmt.Errorf("unsupported type for CompositeField: %T", v)
	}
}

// Ensure JsonMarshaler is still valid if needed, or remove if not part of interface.
// I'll keep MarshalJSON as it's standard go.
func (c *CompositeField) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", string(c.data))), nil
}

func (c *CompositeField) unpackSubfields(content []byte) error {
	offset := 0

	switch c.config.Structure {
	case "tlv":
		for offset < len(content) {
			// A. Parse Tag
			tagName, read, err := c.decodeTag(content[offset:])
			if err != nil {
				return err
			}
			offset += read

			// B. Parse Length
			valLen, read, err := c.decodeTLVLength(content[offset:])
			if err != nil {
				return err
			}
			offset += read

			if offset+valLen > len(content) {
				return fmt.Errorf("TLV field %s: length %d exceeds remaining data %d", tagName, valLen, len(content)-offset)
			}

			// C. Locate Subfield Spec
			f, ok := c.subfields[tagName]
			if !ok {
				// Skipping unknown TLV tag
				offset += valLen
				continue
			}

			// D. Harden Subfield Spec (Inject defaults if missing for Moov compatibility)
			c.hardenField(f)

			// E. Unpack Value
			// In TLV mode, the length is EXPLICIT in the header.
			// We skip decodeFieldLength(f) because it might try to decode a prefix
			// INSIDE the TLV value, causing offset drift.
			prefBytes := 0
			totalRead := valLen

			// Extract value
			raw := content[offset+prefBytes : offset+totalRead]
			val := string(raw)
			c.values[tagName] = val

			// Force populate into field
			if err := f.SetData(val); err != nil {
				return fmt.Errorf("failed to set data for subfield %s: %w", tagName, err)
			}

			offset += totalRead
		}

	case "fixed", "": // Default to positional
		// Iterate ordered fields
		for _, key := range c.ordered {
			if offset >= len(content) {
				break // Optional fields at end?
			}
			f := c.subfields[key]

			valLen, prefBytes, err := c.decodeFieldLength(f, content[offset:])
			if err != nil {
				return fmt.Errorf("failed to decode length for subfield %s: %w", key, err)
			}

			totalRead := prefBytes + valLen
			if offset+totalRead > len(content) {
				return fmt.Errorf("subfield %s exceeds remaining content", key)
			}

			// Extract value
			raw := content[offset+prefBytes : offset+prefBytes+valLen]
			val := string(raw)
			c.values[key] = val

			// Force populate into field
			if err := f.SetData(val); err != nil {
				return fmt.Errorf("failed to set data for subfield %s: %w", key, err)
			}

			offset += totalRead
		}
	}

	return nil
}

// decodeFieldLength is a helper that handles Moov's Fixed prefixer fallback to Spec.Length.
func (c *CompositeField) decodeFieldLength(f field.Field, data []byte) (int, int, error) {
	if f == nil || f.Spec() == nil || f.Spec().Pref == nil {
		return 0, 0, fmt.Errorf("field or spec or prefixer is nil")
	}

	return f.Spec().Pref.DecodeLength(f.Spec().Length, data)
}

func (c *CompositeField) encodeTag(tag string) ([]byte, error) {
	switch c.config.TagEnc {
	case "hex":
		return hex.DecodeString(tag)
	case "ascii":
		return []byte(tag), nil
	case "int":
		i, _ := strconv.Atoi(tag)
		// How many bytes for int tag? Default 1?
		return []byte{byte(i)}, nil
	default:
		return []byte(tag), nil
	}
}

func (c *CompositeField) decodeTag(data []byte) (string, int, error) {
	if len(data) == 0 {
		return "", 0, fmt.Errorf("eof reading tag")
	}

	switch c.config.TagEnc {
	case "hex":
		// Assume 1 or 2 byte tags?
		// BER-TLV has complex tag decoding.
		// For now: if byte1 & 0x1F == 0x1F, it's multi-byte.
		if data[0]&0x1F == 0x1F {
			// Multi-byte (at least 2)
			if len(data) < 2 {
				return "", 0, fmt.Errorf("incomplete multi-byte tag")
			}
			return hex.EncodeToString(data[:2]), 2, nil
		}
		return hex.EncodeToString(data[:1]), 1, nil

	case "ascii":
		// How long is an ASCII tag?
		// We'd need it in config or assume first 2 chars?
		return string(data[:2]), 2, nil

	case "int":
		return strconv.Itoa(int(data[0])), 1, nil

	default:
		return hex.EncodeToString(data[:1]), 1, nil
	}
}

func (c *CompositeField) encodeTLVLength(length int) ([]byte, error) {
	switch c.config.LenEnc {
	case "binary", "int":
		if length < 128 {
			return []byte{byte(length)}, nil
		}
		if length < 256 {
			return []byte{0x81, byte(length)}, nil
		}
		return []byte{0x82, byte(length >> 8), byte(length)}, nil
	case "bcd":
		// 1 byte BCD allows up to 99
		return []byte{byte((length/10)<<4 | (length % 10))}, nil
	default: // ascii
		s := fmt.Sprintf("%02d", length)
		return []byte(s), nil
	}
}

func (c *CompositeField) decodeTLVLength(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("eof reading length")
	}

	switch c.config.LenEnc {
	case "binary", "int":
		b := data[0]
		if b < 0x80 {
			return int(b), 1, nil
		}
		numBytes := int(b & 0x7F)
		if len(data) < 1+numBytes {
			return 0, 0, fmt.Errorf("truncated multi-byte length")
		}
		val := 0
		for i := 0; i < numBytes; i++ {
			val = (val << 8) | int(data[1+i])
		}
		return val, 1 + numBytes, nil

	case "bcd":
		val := int((data[0]>>4)*10 + (data[0] & 0x0F))
		return val, 1, nil

	default: // ascii
		if len(data) < 2 {
			return 0, 0, fmt.Errorf("truncated ascii length")
		}
		val, _ := strconv.Atoi(string(data[:2]))
		return val, 2, nil
	}
}

func (c *CompositeField) SetData(v any) error {
	switch val := v.(type) {
	case string:
		c.data = []byte(val)
	case []byte:
		c.data = val
	default:
		return fmt.Errorf("unsupported type for SetData: %T", v)
	}

	c.syncState()
	return nil
}

func (c *CompositeField) syncState() {
	if len(c.subfields) == 0 || len(c.data) == 0 {
		return
	}

	// Detect if c.data has a prefix (can happen if SetBytes was called with full wire data)
	data := c.data
	payloadLen, prefixLen, err := c.decodeFieldLength(c, data)
	if err == nil && prefixLen+payloadLen == len(data) {
		// Data has prefix, skip it to get to the payload content
		data = data[prefixLen : prefixLen+payloadLen]
	}

	_ = c.unpackSubfields(data)
}

func (c *CompositeField) SetValue(v any) {
	_ = c.SetData(v)
}

// hardenField ensures the field has enough metadata (like encoding)
// to prevent panics in the moov-io library's default packers/unpackers.
func (c *CompositeField) hardenField(f field.Field) {
	if f != nil && f.Spec() != nil {
		if f.Spec().Enc == nil {
			f.Spec().Enc = encoding.ASCII
		}
	}
}
