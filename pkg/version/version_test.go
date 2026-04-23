// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

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
	// Backup
	origDirty := Dirty
	defer func() { Dirty = origDirty }()

	// 1. Clean Info
	Dirty = ""
	info := FullInfo()
	if strings.Contains(info, "-dirty") {
		t.Error("FullInfo should not contain -dirty when Dirty is empty")
	}

	// 2. Dirty Info
	Dirty = "-dirty"
	infoDirty := FullInfo()
	if !strings.Contains(infoDirty, "-dirty") {
		t.Error("FullInfo should contain -dirty when Dirty is set")
	}

	expectedOS := runtime.GOOS
	expectedArch := runtime.GOARCH

	if !strings.Contains(infoDirty, expectedOS) {
		t.Errorf("FullInfo missing OS: %s", infoDirty)
	}
	if !strings.Contains(infoDirty, expectedArch) {
		t.Errorf("FullInfo missing Arch: %s", infoDirty)
	}
}
