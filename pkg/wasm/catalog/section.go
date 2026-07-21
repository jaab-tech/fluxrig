// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var (
	ErrInvalidWasm     = errors.New("invalid wasm binary")
	ErrSectionNotFound = errors.New("custom section not found")
)

// Wasm Header
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// AppendCustomSection appends a custom section to the end of a valid Wasm binary.
func AppendCustomSection(wasmBytes []byte, name string, payload []byte) ([]byte, error) {
	if len(wasmBytes) < 8 || !bytes.Equal(wasmBytes[:8], wasmMagic) {
		return nil, ErrInvalidWasm
	}

	// 1. Build the Section Payload
	// name_len (varuint32) + name (bytes) + payload
	var nameLenBuf [binary.MaxVarintLen32]byte
	nlSize := binary.PutUvarint(nameLenBuf[:], uint64(len(name)))

	sectionPayload := make([]byte, 0, nlSize+len(name)+len(payload))
	sectionPayload = append(sectionPayload, nameLenBuf[:nlSize]...)
	sectionPayload = append(sectionPayload, []byte(name)...)
	sectionPayload = append(sectionPayload, payload...)

	// 2. Build the Section Header
	// id (1 byte) + size (varuint32)
	var sizeBuf [binary.MaxVarintLen32]byte
	sSize := binary.PutUvarint(sizeBuf[:], uint64(len(sectionPayload)))

	// 3. Append to output
	out := make([]byte, 0, len(wasmBytes)+1+sSize+len(sectionPayload))
	out = append(out, wasmBytes...)
	out = append(out, 0x00) // Custom Section ID
	out = append(out, sizeBuf[:sSize]...)
	out = append(out, sectionPayload...)

	return out, nil
}

// ExtractCustomSection searches for a custom section by name, returns its payload,
// and returns a new byte slice with that section stripped.
func ExtractCustomSection(wasmBytes []byte, targetName string) ([]byte, []byte, error) {
	if len(wasmBytes) < 8 || !bytes.Equal(wasmBytes[:8], wasmMagic) {
		return nil, nil, ErrInvalidWasm
	}

	offset := 8
	for offset < len(wasmBytes) {
		sectionStart := offset

		// Read Section ID
		id := wasmBytes[offset]
		offset++

		// Read Section Size
		size, n := binary.Uvarint(wasmBytes[offset:])
		if n <= 0 {
			return nil, nil, ErrInvalidWasm
		}
		offset += n

		if offset+int(size) > len(wasmBytes) {
			return nil, nil, ErrInvalidWasm
		}

		payloadStart := offset
		offset += int(size)

		// Check if it's a Custom Section (0)
		if id == 0 {
			// Read Name Length
			nameLen, nn := binary.Uvarint(wasmBytes[payloadStart:])
			if nn > 0 && payloadStart+nn+int(nameLen) <= offset {
				nameStart := payloadStart + nn
				name := string(wasmBytes[nameStart : nameStart+int(nameLen)])

				if name == targetName {
					customDataStart := nameStart + int(nameLen)
					customData := wasmBytes[customDataStart:offset]

					// Strip the section: concat everything before sectionStart and after offset
					stripped := make([]byte, 0, len(wasmBytes)-(offset-sectionStart))
					stripped = append(stripped, wasmBytes[:sectionStart]...)
					stripped = append(stripped, wasmBytes[offset:]...)

					return customData, stripped, nil
				}
			}
		}
	}

	return nil, nil, ErrSectionNotFound
}
