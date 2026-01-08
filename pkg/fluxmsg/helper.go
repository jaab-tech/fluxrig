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

package fluxmsg

import (
	"fmt"
	"strings"
)

// Helper methods to make working with generic maps easier

// Set stores a value regardless of nesting (using dot notation)
// Example: msg.Set("user.address.zip", 12345)
func (m *FluxMsg) Set(key string, val any) error {
	parts := strings.Split(key, ".")
	if len(parts) == 1 {
		m.Data[key] = val
		return nil
	}

	// Navigate to the leaf
	current := m.Data
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		next, ok := current[part]
		if !ok {
			// Create new map
			newMap := make(map[string]any)
			current[part] = newMap
			current = newMap
		} else {
			// Type assert
			asMap, ok := next.(map[string]any)
			if !ok {
				return fmt.Errorf("key conflict at '%s': not a map", part)
			}
			current = asMap
		}
	}

	// Set value
	current[parts[len(parts)-1]] = val
	return nil
}

// Get retrieves a value using dot notation
func (m *FluxMsg) Get(key string) (any, bool) {
	parts := strings.Split(key, ".")
	current := m.Data

	for i := 0; i < len(parts)-1; i++ {
		next, ok := current[parts[i]]
		if !ok {
			return nil, false
		}
		asMap, ok := next.(map[string]any)
		if !ok {
			return nil, false
		}
		current = asMap
	}

	val, ok := current[parts[len(parts)-1]]
	return val, ok
}

// GetString helper
func (m *FluxMsg) GetString(key string) (string, error) {
	v, ok := m.Get(key)
	if !ok {
		return "", fmt.Errorf("key not found: %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("key %s is not a string", key)
	}
	return s, nil
}
