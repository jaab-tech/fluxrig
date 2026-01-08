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
	"path/filepath"
	"testing"
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
