// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestWAL_WriteRead(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := Open(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to open WAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write
	payload := map[string]string{"foo": "bar"}
	if errWrite := w.Write(payload); errWrite != nil {
		t.Fatalf("Write failed: %v", errWrite)
	}

	// Read
	// First index should be 1
	data, err := w.Read(1)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	var res map[string]string
	if err := msgpack.Unmarshal(data, &res); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if res["foo"] != "bar" {
		t.Errorf("Expected bar, got %s", res["foo"])
	}
}

func TestWAL_Truncate(t *testing.T) {
	tmpDir := t.TempDir()
	w, err := Open(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to open WAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write 10 items
	for i := 0; i < 10; i++ {
		_ = w.Write(i)
	}

	last, _ := w.LastIndex()
	if last != 10 {
		t.Errorf("Expected last index 10, got %d", last)
	}

	// Truncate before 5
	if errTrunc := w.TruncateFront(5); errTrunc != nil {
		t.Fatalf("TruncateFront failed: %v", errTrunc)
	}

	// Try reading 1 (should fail)
	_, errTrunc := w.Read(1)
	if errTrunc == nil {
		t.Error("Read(1) should fail after truncate")
	}

	// Try reading 5 (should succeed)
	_, err = w.Read(5)
	if err != nil {
		t.Errorf("Read(5) failed: %v", err)
	}
}

// Legacy Test compatibility (removed, creating new tests)
