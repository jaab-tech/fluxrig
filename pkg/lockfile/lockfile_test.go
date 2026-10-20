// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "flux.lock")

	// 1. Acquire
	l1, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("First acquire failed: %v", err)
	}

	// 2. Acquire Again (Should Fail ?)
	// Since we are in the same process, flock behavior depends on OS implementation regarding fd.
	// Usually flock is associated with the file descriptor.
	// Acquire opens a NEW file descriptor.
	// So flock on new fd should fail if locked exclusive by another fd.
	_, err2 := Acquire(lockPath)
	if err2 == nil {
		t.Error("Expected error acquiring locked file twice")
	}

	// 3. Release
	if err := l1.Release(); err != nil {
		t.Errorf("Release failed: %v", err)
	}

	// 4. Acquire Again (Should Succeed)
	l3, err3 := Acquire(lockPath)
	if err3 != nil {
		t.Fatalf("Re-acquire failed: %v", err3)
	}
	_ = l3.Release()
}

// AcquireWithTimeout must wait for a held lock and take it once it is free.
// This is what lets a second index writer queue behind the first and merge,
// instead of failing or overwriting.
func TestAcquireWithTimeout_WaitsForRelease(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "index.lock")

	held, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Release after a short delay; the waiter must take the lock well within
	// a generous timeout.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = held.Release()
	}()

	start := time.Now()
	waited, err := AcquireWithTimeout(lockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("AcquireWithTimeout failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("AcquireWithTimeout returned too fast (%s); it did not wait for the release", elapsed)
	}
	_ = waited.Release()
}

// A lock that is never released must time out, and the error must say so.
func TestAcquireWithTimeout_TimesOut(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "index.lock")

	held, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer func() { _ = held.Release() }()

	_, err = AcquireWithTimeout(lockPath, 150*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout when the lock is held for the whole window")
	}
}
