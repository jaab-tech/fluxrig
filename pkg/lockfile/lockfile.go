// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock represents an active file lock.
type Lock struct {
	file *os.File
}

// Acquire attempts to acquire an exclusive lock on the specified path.
// It returns a Lock object that must be released by calling Release().
// If the lock is already held by another process, it returns an error.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", path, err)
	}

	// LOCK_EX = Exclusive lock; LOCK_NB = non-blocking (fail if unable to acquire).
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("lock already held by another process")
		}
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	return &Lock{file: f}, nil
}

// AcquireWithTimeout waits up to timeout for an exclusive lock on path, then
// gives up. A caller that must not drop a write uses this instead of Acquire:
// waiting turns two concurrent writers into a queue, so the second one merges
// with the first instead of failing or overwriting it.
func AcquireWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: f}, nil
		}
		if err != syscall.EWOULDBLOCK {
			_ = f.Close()
			return nil, fmt.Errorf("failed to acquire lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("timed out after %s waiting for the lock on %s", timeout, path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Release releases the lock and closed the underlying file.
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}
	// Closing the file descriptor automatically releases the flock
	err := l.file.Close()
	l.file = nil
	return err
}
