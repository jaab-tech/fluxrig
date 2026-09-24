// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package path

import (
	"path/filepath"
	"testing"
)

func TestSanitize_EmptyPath_ReturnsError(t *testing.T) {
	_, err := Sanitize("")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestSanitize_CleansPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/foo/bar/../baz", "/foo/baz"},
		{"./foo/bar", "foo/bar"},
		{"foo//bar", "foo/bar"},
		{"foo/./bar", "foo/bar"},
		{"foo/bar/.", "foo/bar"},
		{"", ""}, // this should error
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if tc.input == "" {
				_, err := Sanitize(tc.input)
				if err == nil {
					t.Error("expected error for empty path")
				}
				return
			}
			result, err := Sanitize(tc.input)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestSanitize_RejectsNullByte(t *testing.T) {
	_, err := Sanitize("foo\x00bar")
	if err == nil {
		t.Error("expected error for path with null byte")
	}
}

func TestSecureJoin_Basic(t *testing.T) {
	result, err := SecureJoin("/base", "file.txt")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	expected := filepath.Join("/base", "file.txt")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestSecureJoin_PreventsTraversal(t *testing.T) {
	tests := []struct {
		base       string
		input      string
		shouldFail bool
	}{
		{"/base", "../etc/passwd", true},
		{"/base", "foo/../../etc/passwd", true},
		{"/base", "foo/bar", false},
		{"/base", "foo/../bar", false},
		{"/base", "foo/../", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			_, err := SecureJoin(tc.base, tc.input)
			if tc.shouldFail {
				if err == nil {
					t.Errorf("expected error for traversal attempt: %s", tc.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestSecureJoin_EmptyBase_ReturnsError(t *testing.T) {
	_, err := SecureJoin("", "file.txt")
	if err == nil {
		t.Error("expected error for empty base")
	}
}

func TestSecureJoin_EmptyInput(t *testing.T) {
	result, err := SecureJoin("/base", "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "/base" {
		t.Errorf("expected /base, got %q", result)
	}
}
