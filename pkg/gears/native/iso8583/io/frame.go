// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"encoding/hex"
	"fmt"
)

// FrameInfo holds metadata extracted during heuristic validation.
type FrameInfo struct {
	MTI          string // MTI as hex string
	MTILen       int    // Bytes consumed by MTI (2 or 4)
	BitmapCount  int    // Number of bitmaps (1, 2, or 3)
	ActiveFields []int  // List of active field numbers
	Valid        bool   // Passed heuristic validation
}

// extractBitmaps recursively reads primary, secondary, and tertiary bitmaps.
// Returns the count of bitmaps, list of active field numbers, and bytes consumed.
func extractBitmaps(data []byte, encoding string) (int, []int, int) {
	fields := make([]int, 0)
	bitmapCount := 0
	offset := 0

	if len(data) < 8 {
		return 0, fields, 0
	}

	// Detect if this is a HEX bitmap (16 bytes representing 64 bits)
	// Some variants (e.g. Mastercard SMS) or test generators (like our python script) use this.
	isHex := false
	if len(data) >= 16 {
		isHex = true
		for i := 0; i < 16; i++ {
			if !isHexChar(data[i], encoding) {
				isHex = false
				break
			}
		}
	}

	if isHex {
		// Process Primary Hex Bitmap (16 chars)
		pMap, _ := decodeHexBitmap(data[offset:offset+16], encoding)
		fields = append(fields, extractFieldsFromBitmap(pMap, 0)...)
		bitmapCount++
		offset += 16

		// Check bit 1 for secondary
		if len(fields) > 0 && fields[0] == 1 && len(data) >= offset+16 {
			sMap, _ := decodeHexBitmap(data[offset:offset+16], encoding)
			fields = append(fields, extractFieldsFromBitmap(sMap, 64)...)
			bitmapCount++
			offset += 16

			// Check bit 65 for tertiary
			hasTertiary := false
			for _, f := range fields {
				if f == 65 {
					hasTertiary = true
					break
				}
			}
			if hasTertiary && len(data) >= offset+16 {
				tMap, _ := decodeHexBitmap(data[offset:offset+16], encoding)
				fields = append(fields, extractFieldsFromBitmap(tMap, 128)...)
				bitmapCount++
				offset += 16
			}
		}
		return bitmapCount, fields, offset
	}

	// Binary Bitmap (Normal)
	pMap := data[offset : offset+8]
	fields = append(fields, extractFieldsFromBitmap(pMap, 0)...)
	bitmapCount++
	offset += 8

	// Check bit 1 (MSB of first byte) for secondary bitmap
	if pMap[0]&0x80 != 0 && len(data) >= offset+8 {
		sMap := data[offset : offset+8]
		fields = append(fields, extractFieldsFromBitmap(sMap, 64)...)
		bitmapCount++
		offset += 8

		// Check bit 1 of secondary for tertiary bitmap
		if sMap[0]&0x80 != 0 && len(data) >= offset+8 {
			tMap := data[offset : offset+8]
			fields = append(fields, extractFieldsFromBitmap(tMap, 128)...)
			bitmapCount++
			offset += 8
		}
	}

	return bitmapCount, fields, offset
}

func isHexChar(b byte, encoding string) bool {
	if encoding == "ebcdic" {
		// EBCDIC '0'-'9' (0xF0-0xF9), 'A'-'F' (0xC1-0xC6), 'a'-'f' (0x81-0x86)
		return (b >= 0xF0 && b <= 0xF9) || (b >= 0xC1 && b <= 0xC6) || (b >= 0x81 && b <= 0x86)
	}
	// ASCII
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'F') || (b >= 'a' && b <= 'f')
}

func decodeHexBitmap(data []byte, encoding string) ([]byte, error) {
	if len(data) != 16 {
		return make([]byte, 8), fmt.Errorf("invalid hex bitmap length")
	}
	res := make([]byte, 8)
	for i := 0; i < 8; i++ {
		high := decodeHexNibble(data[i*2], encoding)
		low := decodeHexNibble(data[i*2+1], encoding)
		res[i] = (high << 4) | low
	}
	return res, nil
}

func decodeHexNibble(b byte, encoding string) byte {
	if encoding == "ebcdic" {
		if b >= 0xF0 && b <= 0xF9 {
			return b - 0xF0
		}
		if b >= 0xC1 && b <= 0xC6 {
			return b - 0xC1 + 10
		}
		if b >= 0x81 && b <= 0x86 {
			return b - 0x81 + 10
		}
		return 0
	}
	if b >= '0' && b <= '9' {
		return b - '0'
	}
	if b >= 'A' && b <= 'F' {
		return b - 'A' + 10
	}
	if b >= 'a' && b <= 'f' {
		return b - 'a' + 10
	}
	return 0
}

// extractFieldsFromBitmap returns active field numbers for a single 8-byte bitmap.
// offset: 0 for primary, 64 for secondary, 128 for tertiary.
func extractFieldsFromBitmap(bitmap []byte, offset int) []int {
	fields := make([]int, 0)
	for i, b := range bitmap {
		if b == 0 {
			continue
		}
		for bit := 0; bit < 8; bit++ {
			if b&(0x80>>bit) != 0 {
				fieldNum := offset + (i * 8) + bit + 1
				fields = append(fields, fieldNum)
			}
		}
	}
	return fields
}

// formatFieldList formats a list of field numbers as a compact string.
// Example: [2,4,7,11,12,14,22,23,25,26]
func formatFieldList(fields []int) string {
	if len(fields) == 0 {
		return "[]"
	}
	result := "["
	for i, f := range fields {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf("%d", f)
	}
	result += "]"
	return result
}

// decodeMTI attempts to interpret the MTI bytes.
// Since we only peek 2 bytes in inspect(), we handle:
// 1. BCD: 0x0200 -> "0200"
// 2. ASCII: 0x3032 -> "02.." (incomplete peek)
// To support full ASCII MTI in inspect(), we need to peek 4 bytes.
// decodeMTI attempts to interpret the MTI bytes based on encoding.
// Returns the decoded MTI string and number of bytes consumed.
func decodeMTI(payload []byte, offset int, encoding string) (string, int) {
	if len(payload) < offset+2 {
		return "", 0
	}

	// 1. EBCDIC Support (e.g. Visa V.I.P)
	if encoding == "ebcdic" && len(payload) >= offset+4 {
		// Convert 4 bytes from EBCDIC to ASCII
		res := make([]byte, 4)
		for i := 0; i < 4; i++ {
			b := payload[offset+i]
			// Map 0xF0-0xF9 to 0x30-0x39 (ASCII '0'-'9')
			if b >= 0xF0 && b <= 0xF9 {
				res[i] = b - 0xC0 // 0xF0 - 0xC0 = 0x30
			} else {
				res[i] = '?' // Unknown/Invalid char
			}
		}
		return string(res), 4
	}

	// 2. BCD Support (e.g. Hypercom/Terminals/Mastercard SMS)
	// 0x02 0x00 -> "0200"
	if encoding == "bcd" {
		b0 := payload[offset]
		b1 := payload[offset+1]
		return fmt.Sprintf("%02X%02X", b0, b1), 2
	}

	// 3. ASCII (Standard) or Generic Heuristic
	// Standard ASCII: 0x30 0x32 0x30 0x30 -> "0200"

	// Helper heuristic for "Generic": Check if it looks like BCD
	// BCD MTIs usually start with 0x01..0x19 (for 01xx..19xx)
	b0 := payload[offset]
	if encoding == "generic" {
		// If first byte is not an ASCII digit ('0'-'9' are 0x30-0x39)
		// but it is a valid BCD byte (0x01-0x19), it's likely BCD.
		if b0 < 0x30 && b0 > 0x00 {
			b1 := payload[offset+1]
			return fmt.Sprintf("%02X%02X", b0, b1), 2
		}
	}

	// Default/ASCII: Return the string directly if length permits
	if len(payload) >= offset+4 {
		// Verify if it looks like digits
		isDigits := true
		for i := 0; i < 4; i++ {
			if payload[offset+i] < '0' || payload[offset+i] > '9' {
				isDigits = false
				break
			}
		}
		if isDigits {
			return string(payload[offset : offset+4]), 4
		}
	}

	// Fallback to hex dump of first 2 bytes if we can't decide
	return fmt.Sprintf("%X", payload[offset:offset+2]), 2
}

// ExtractHeader parses the header based on variant and extracts metadata.
// Returns a map of metadata keys to values (e.g. "iso8583.src_id", "iso8583.visa.format_id").
func ExtractHeader(payload []byte, cfg *Config) map[string]string {
	meta := make(map[string]string)

	if cfg.Variant == VariantVisa {
		// Visa V.I.P Header (Standard: 22 bytes, Reject: 26 bytes)
		// Per VisaNet Authorization-Only Online Messages Technical Specifications

		if len(payload) >= 22 {
			// Field 1: Header Length
			headerLen := int(payload[0])
			meta["iso8583.visa.header_length"] = fmt.Sprintf("%d", headerLen)

			// Field 2: Header Format
			meta["iso8583.visa.header_format"] = fmt.Sprintf("%02X", payload[1])

			// Field 3: Text Format
			meta["iso8583.visa.text_format"] = fmt.Sprintf("%02X", payload[2])

			// Field 4: Total Message Length (Big Endian)
			msgLen := int(payload[3])<<8 | int(payload[4])
			meta["iso8583.visa.message_length"] = fmt.Sprintf("%d", msgLen)

			// Field 5: Destination ID (3 bytes, BCD)
			meta["iso8583.dst_id"] = fmt.Sprintf("%02X%02X%02X", payload[5], payload[6], payload[7])

			// Field 6: Source ID (3 bytes, BCD)
			meta["iso8583.src_id"] = fmt.Sprintf("%02X%02X%02X", payload[8], payload[9], payload[10])

			// Field 7: Round-Trip Control Info
			meta["iso8583.visa.round_trip"] = fmt.Sprintf("%02X", payload[11])

			// Field 8: V.I.P. Flags (2 bytes)
			meta["iso8583.visa.flags"] = fmt.Sprintf("%02X%02X", payload[12], payload[13])

			// Field 9: Message Status Flags (3 bytes)
			meta["iso8583.visa.status_flags"] = fmt.Sprintf("%02X%02X%02X", payload[14], payload[15], payload[16])

			// Field 10: Batch Number
			meta["iso8583.visa.batch_number"] = fmt.Sprintf("%02X", payload[17])

			// Field 11: Reserved (Bytes 18-20) - skip

			// Field 12: User Information
			meta["iso8583.visa.user_info"] = fmt.Sprintf("%02X", payload[21])

			// Check for Reject Header (26 bytes)
			if headerLen >= 26 && len(payload) >= 26 {
				// Field 13: Bitmap (indicates Field 14 presence)
				meta["iso8583.visa.reject_bitmap"] = fmt.Sprintf("%02X%02X", payload[22], payload[23])

				// Field 14: Reject Data Group (4-digit reject code)
				meta["iso8583.visa.reject_code"] = fmt.Sprintf("%02X%02X", payload[24], payload[25])
			}
		}
	}

	return meta
}

// GetHeaderOffset returns the byte offset where the ISO8583 message starts.
func GetHeaderOffset(payload []byte, cfg *Config) int {
	offset := 0
	if cfg.TPDUEnabled && len(payload) > cfg.TPDULength {
		offset = cfg.TPDULength
	} else if cfg.ProtocolHeaderSize > 0 && len(payload) > cfg.ProtocolHeaderSize {
		offset = cfg.ProtocolHeaderSize
	}
	return offset
}

// BuildHeader constructs a protocol-specific header.
// payloadLen is the length of the ISO8583 message (MTI + Bitmaps + Fields) excluding this header.
func BuildHeader(cfg *Config, dynamicSrc, dynamicDst string, payloadLen int) []byte {
	switch cfg.Variant {
	case VariantVisa:
		return buildVisaHeader(cfg, dynamicSrc, dynamicDst, payloadLen)
	case VariantMastercard:
		return nil // No header (formerly MIP/SMS, both 2-byte framing)
	}
	return nil
}

func buildVisaHeader(cfg *Config, dynSrc, dynDst string, payloadLen int) []byte {
	// 22-byte Standard V.I.P Header
	h := make([]byte, 22)

	// Field 1: Header Length (Hexadecimal)
	h[0] = 0x16

	// Field 2: Header Format (0x01 = Standard Processor-to-VisaNet)
	h[1] = 0x01

	// Field 3: Text Format (0x02 = Visa standard format)
	h[2] = 0x02

	// Field 4: Total Message Length (Big Endian)
	// Includes header (22) + payload
	totalLen := 22 + payloadLen
	h[3] = byte(totalLen >> 8)
	h[4] = byte(totalLen)

	// Field 5: Destination ID (3 bytes BCD)
	dst := dynDst
	if dst == "" {
		dst = cfg.VisaDstID
	}
	copy(h[5:8], hexToBytesFixed(dst, 3))

	// Field 6: Source ID (3 bytes BCD)
	src := dynSrc
	if src == "" {
		src = cfg.VisaSrcID
	}
	copy(h[8:11], hexToBytesFixed(src, 3))

	// Fields 7-12 are typically zeros for outgoing requests
	// h[11] = Round-Trip Control
	// h[12:14] = V.I.P. Flags
	// h[14:17] = Message Status Flags
	// h[17] = Batch Number
	// h[18:21] = Reserved
	// h[21] = User Information

	return h
}

// hexToBytesFixed decodes hex string to N bytes. Pads/Truncates if needed.
func hexToBytesFixed(s string, n int) []byte {
	res := make([]byte, n)
	var data []byte
	var err error

	// If input is odd length, pad with leading zero
	if len(s)%2 != 0 {
		s = "0" + s
	}

	if len(s) > 0 {
		data, err = hex.DecodeString(s)
		if err == nil {
			copy(res, data)
		}
	}
	return res
}
