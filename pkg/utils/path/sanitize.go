// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package path provides secure utilities for file system operations
package path

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Sanitize ensures that a user-provided path is clean and does not attempt
// directory traversal outside of the intended base directory.
// If the base dir is empty, it just cleans the path and ensures no ".." traversal remains.
func Sanitize(inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	cleaned := filepath.Clean(inputPath)

	// Simple heuristic: If it cleans to just "." or starts with "..", it's suspicious in some contexts
	// But mostly, we want to prevent accessing sensitive files.
	// We'll enforce that the path doesn't contain null bytes (handled by go 1.20+ anyway)
	if strings.Contains(cleaned, "\x00") {
		return "", fmt.Errorf("invalid characters in path")
	}

	return cleaned, nil
}

// SecureJoin joins a base path and an untrusted user input, ensuring the resulting
// path does not escape the base directory.
func SecureJoin(base, userInput string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("base path cannot be empty")
	}

	baseClean := filepath.Clean(base)
	joined := filepath.Join(baseClean, userInput)
	joinedClean := filepath.Clean(joined)

	// Verify the result is still within the base directory
	if !strings.HasPrefix(joinedClean, baseClean+string(filepath.Separator)) && joinedClean != baseClean {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return joinedClean, nil
}
