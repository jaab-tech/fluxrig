// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"testing"
)

func TestAppendAndExtractCustomSection(t *testing.T) {
	// A minimal valid Wasm binary header (magic number + version)
	validWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

	sectionName := "fluxrig.signature"
	payloadData := []byte("fake-ed25519-signature-bytes")

	// Test Appending
	appendedWasm, err := AppendCustomSection(validWasm, sectionName, payloadData)
	if err != nil {
		t.Fatalf("failed to append custom section: %v", err)
	}

	if len(appendedWasm) <= len(validWasm) {
		t.Fatalf("appended wasm should be larger than original")
	}

	// Test Extracting
	extractedPayload, strippedWasm, err := ExtractCustomSection(appendedWasm, sectionName)
	if err != nil {
		t.Fatalf("failed to extract custom section: %v", err)
	}

	if string(extractedPayload) != string(payloadData) {
		t.Errorf("expected payload %q, got %q", payloadData, extractedPayload)
	}

	if !bytes.Equal(strippedWasm, validWasm) {
		t.Errorf("stripped wasm does not match original")
	}
}

func TestExtractCustomSection_NotFound(t *testing.T) {
	validWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

	// Test extraction on vanilla wasm
	_, _, err := ExtractCustomSection(validWasm, "fluxrig.signature")
	if err != ErrSectionNotFound {
		t.Fatalf("expected ErrSectionNotFound, got %v", err)
	}
}

func TestAppendCustomSection_InvalidWasm(t *testing.T) {
	invalidWasm := []byte{0x00, 0x00, 0x00}

	_, err := AppendCustomSection(invalidWasm, "test", []byte("data"))
	if err != ErrInvalidWasm {
		t.Fatalf("expected ErrInvalidWasm, got %v", err)
	}

	_, _, err = ExtractCustomSection(invalidWasm, "test")
	if err != ErrInvalidWasm {
		t.Fatalf("expected ErrInvalidWasm, got %v", err)
	}
}

func TestExtractCustomSection_MultipleSections(t *testing.T) {
	validWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

	// Append two different sections
	w1, _ := AppendCustomSection(validWasm, "first.section", []byte("data1"))
	w2, _ := AppendCustomSection(w1, "second.section", []byte("data2"))

	// Extract the first section
	payload1, stripped1, err1 := ExtractCustomSection(w2, "first.section")
	if err1 != nil {
		t.Fatalf("failed to extract first section: %v", err1)
	}
	if string(payload1) != "data1" {
		t.Errorf("expected data1, got %q", payload1)
	}

	// The stripped version should still contain the second section
	payload2, _, err2 := ExtractCustomSection(stripped1, "second.section")
	if err2 != nil {
		t.Fatalf("failed to extract second section from stripped wasm: %v", err2)
	}
	if string(payload2) != "data2" {
		t.Errorf("expected data2, got %q", payload2)
	}
}
