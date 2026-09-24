// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"fmt"
	"strings"
)

// Helper methods to make working with generic maps easier

// AsDataMap accepts either shape a nested object in Data can take on a
// FluxMsg: a message built in process carries map[string]any, but CBOR
// decodes an object into map[any]any, so the same field is one shape before
// a bus hop and the other after it. Descending only into map[string]any
// therefore makes a nested path resolve locally and fail once the message
// has crossed the bus, with no error anywhere: the caller simply sees a
// missing field. The one place this normalization lives: pkg/sdk and
// pkg/gears/native/coatcheck both call it rather than each keeping their own
// copy, which had already drifted once (see coatcheck's restore.go history).
func AsDataMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

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
