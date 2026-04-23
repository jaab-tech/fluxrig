// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

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

	var current any = m.Data
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				newMap := make(map[string]any)
				v[part] = newMap
				current = newMap
			} else {
				switch next.(type) {
				case map[string]any, map[any]any:
					current = next
				default:
					return fmt.Errorf("key conflict at '%s': not a map", part)
				}
			}
		case map[any]any:
			next, ok := v[part]
			if !ok {
				newMap := make(map[string]any)
				v[part] = newMap
				current = newMap
			} else {
				switch next.(type) {
				case map[string]any, map[any]any:
					current = next
				default:
					return fmt.Errorf("key conflict at '%s': not a map", part)
				}
			}
		default:
			return fmt.Errorf("key conflict at '%s': not a map", part)
		}
	}

	part := parts[len(parts)-1]
	switch v := current.(type) {
	case map[string]any:
		v[part] = val
	case map[any]any:
		v[part] = val
	default:
		return fmt.Errorf("key conflict at leaf: not a map")
	}

	return nil
}

// Get retrieves a value using dot notation
func (m *FluxMsg) Get(key string) (any, bool) {
	parts := strings.Split(key, ".")
	var current any = m.Data

	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return nil, false
			}
			switch next.(type) {
			case map[string]any, map[any]any:
				current = next
			default:
				return nil, false
			}
		case map[any]any:
			next, ok := v[part]
			if !ok {
				return nil, false
			}
			switch next.(type) {
			case map[string]any, map[any]any:
				current = next
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}

	part := parts[len(parts)-1]
	switch v := current.(type) {
	case map[string]any:
		val, ok := v[part]
		return val, ok
	case map[any]any:
		val, ok := v[part]
		return val, ok
	}

	return nil, false
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
