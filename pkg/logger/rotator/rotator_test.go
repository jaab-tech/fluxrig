package rotator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotator(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	// 1. Create Rotator (Small limit: 10 bytes)
	r, err := New(logPath, 0, 2, false) // 0MB effectively 0 bytes?
	// maxSizeMB is int. 0 would mean 0 bytes.
	// If I pass 0, size+writeLen > 0 always true?
	// Let's check impl: maxSizeBytes = maxSizeMB * 1024 * 1024.
	// If maxSizeMB=0, maxSizeBytes=0.
	// Line 76: r.maxSize > 0 && ...
	// So 0 means UNLIMITED.
	// I need to use mock or modify impl locally? No.
	// New takes int maxSizeMB.
	// So minimum restriction is 1MB.
	// I cannot test rotation with New unless I write > 1MB.
	// But I can construct directly or write 1MB.
	// Writing 1MB is fast.

	r, err = New(logPath, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	// Hack: adjust maxSize via reflection? No.
	// Construct struct manually? r is pointer.
	r.maxSize = 10 // 10 bytes
	defer r.Close()

	// 2. Write 5 bytes
	if _, err := r.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}

	// 3. Write 6 bytes -> Trigger Rotation
	// "12345" (5) + "123456" (6) = 11 > 10.
	if _, err := r.Write([]byte("123456")); err != nil {
		t.Fatal(err)
	}

	// Check files
	entries, _ := os.ReadDir(tmpDir)
	// Should have: test.log (active) and test.log.TIMESTAMP
	if len(entries) < 2 {
		t.Errorf("Expected rotation, got %d files", len(entries))
	}
}

func TestCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "cleanup.log")

	// Create dummy backups
	base := logPath
	os.Create(base + ".1")
	time.Sleep(10 * time.Millisecond)
	os.Create(base + ".2")
	time.Sleep(10 * time.Millisecond)
	os.Create(base + ".3")

	r := &Rotator{
		filename:   logPath,
		maxBackups: 1, // Keep only 1
		compress:   true,
	}

	// Run cleanup on .3, effectively compressing it and removing old (.1, .2)
	// cleanup(newBackup)
	// Wait, cleanup takes the NEWEST backup name.
	r.cleanup(base + ".3")

	// Wait for async cleanup (it spawns go routine? No, I called it directly but in code it is `go r.cleanup`).
	// In test I called unexported method directly? Ensure visibility. `rotator_test` same package.
	// The implementation `go r.cleanup` is in `rotate`.
	// Here I called it synchronously.

	// Verify .1 and .2 are gone?
	// Sort: .1, .2, .3
	// .3 is newest.
	// Keep 1 -> Keep .3 (or .3.gz)
	// Delete .1, .2

	if _, err := os.Stat(base + ".1"); !os.IsNotExist(err) {
		t.Error(".1 should be deleted")
	}
	if _, err := os.Stat(base + ".3.gz"); os.IsNotExist(err) {
		t.Error(".3 should be compressed")
	}
}
