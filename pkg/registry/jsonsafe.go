// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"math"
)

// JSONSafe converts what CBOR decoding produces into what JSON can encode:
// maps keyed by interface{} become maps keyed by string, slices are walked,
// and NaN or an infinity (which JSON has no form for) becomes null. A value
// of any other type that JSON refuses becomes its printed form.
func JSONSafe(v any) any {
	switch t := v.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return t
	case float32:
		return JSONSafe(float64(t))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = JSONSafe(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = JSONSafe(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = JSONSafe(val)
		}
		return out
	default:
		if _, err := json.Marshal(t); err == nil {
			return t
		}
		return fmt.Sprint(t)
	}
}

// JSONSafeMap applies JSONSafe to a map, keeping nil as nil.
func JSONSafeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return JSONSafe(m).(map[string]any)
}
