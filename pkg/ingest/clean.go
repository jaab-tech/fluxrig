package ingest

import "fmt"

func cleanMap(m interface{}) interface{} {
	switch v := m.(type) {
	case map[interface{}]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[fmt.Sprint(k)] = cleanMap(val)
		}
		return res
	case map[string]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[k] = cleanMap(val)
		}
		return res
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, val := range v {
			res[i] = cleanMap(val)
		}
		return res
	default:
		return v
	}
}
