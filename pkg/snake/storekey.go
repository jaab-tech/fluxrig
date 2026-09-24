// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MinStoreKeyLen is the shortest key file the Snake accepts, in characters after
// trimming. A shorter one is a password, and this key protects every message.
const MinStoreKeyLen = 32

// LoadStoreKeyFile reads a store key the operator provides. Surrounding white space
// is ignored. A key shorter than MinStoreKeyLen is refused.
func LoadStoreKeyFile(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("snake: read store key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if len(key) < MinStoreKeyLen {
		return "", fmt.Errorf("snake: store key file %s holds %d characters, at least %d are required", path, len(key), MinStoreKeyLen)
	}
	return key, nil
}
