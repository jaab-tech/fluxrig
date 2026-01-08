// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock represents an active file lock.
type Lock struct {
	file *os.File
}

// Acquire attempts to acquire an exclusive lock on the specified path.
// It returns a Lock object that must be released by calling Release().
// If the lock is already held by another process, it returns an error.
func Acquire(path string) (*Lock, error) {
	// 1. Open or create the lock file
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", path, err)
	}

	// 2. Attempt to Flock (Exclusive, Non-blocking)
	// LOCK_EX = Exclusive lock
	// LOCK_NB = Non-blocking (fail if unable to acquire)
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
