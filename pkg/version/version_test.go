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

package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	// Backup original values
	origVersion := Version
	origCommit := Commit
	origDirty := Dirty
	defer func() {
		Version = origVersion
		Commit = origCommit
		Dirty = origDirty
	}()

	tests := []struct {
		name    string
		version string
		commit  string
		dirty   string
		want    string
	}{
		{
			name:    "Clean Release",
			version: "1.0.0",
			commit:  "abc1234",
			dirty:   "",
			want:    "1.0.0+abc1234",
		},
		{
			name:    "Dirty Dev",
			version: "0.0.0-dev",
			commit:  "def5678",
			dirty:   "-dirty",
			want:    "0.0.0-dev+def5678-dirty",
		},
		{
			name:    "No Commit",
			version: "0.1.0",
			commit:  "none",
			dirty:   "",
			want:    "0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			Commit = tt.commit
			Dirty = tt.dirty
			if got := String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFullInfo(t *testing.T) {
	// Smoke test for format
	// Current implementation: fmt.Sprintf("FluxRig %s (%s, %s/%s)", String(), BuildDate, runtime.GOOS, runtime.GOARCH)

	info := FullInfo()
	if len(info) == 0 {
		t.Error("FullInfo returned empty string")
	}
	expectedOS := runtime.GOOS
	expectedArch := runtime.GOARCH

	if !strings.Contains(info, expectedOS) {
		t.Errorf("FullInfo missing OS: %s", info)
	}
	if !strings.Contains(info, expectedArch) {
		t.Errorf("FullInfo missing Arch: %s", info)
	}
}
