// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"strings"
)

// configEnvAliases are top-level environment variable shortcuts that are not
// part of any config struct's own shape: FLUXRIG_TRACE instead of the fully
// qualified FLUXRIG_LOGGING_TRACE.
var configEnvAliases = map[string]string{
	"trace":             "logging.trace",
	"debug":             "logging.debug",
	"disable_telemetry": "telemetry.disabled",
}

// buildEnvKeyMap walks cfg's struct tree following its "koanf" tags and
// returns a map from the environment-variable spelling of every leaf key
// (its real, correctly-nested dotted koanf path, lower-cased with dots
// replaced by underscores) back to that real dotted path.
//
// A flat env var name cannot be split into section and field by guessing
// where the dots go: FLUXRIG_SNAKE_STREAM_MAX_BYTES has one field,
// "stream_max_bytes", not three. Building the map from the config struct's
// own shape, instead of a hand-maintained prefix list, is what keeps every
// multi-word field reachable without drifting out of sync as fields are
// added — a hand-maintained list already had exactly this gap once: no
// "telemetry_" prefix, so every multi-word telemetry.* field silently
// matched nothing.
func buildEnvKeyMap(cfg any) map[string]string {
	m := make(map[string]string)
	walkKoanfFields(reflect.TypeOf(cfg), nil, m)
	return m
}

func walkKoanfFields(t reflect.Type, path []string, m map[string]string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("koanf")
		if tag == "" || tag == "-" {
			continue
		}
		fieldPath := append(append([]string{}, path...), tag)

		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkKoanfFields(ft, fieldPath, m)
			continue
		}

		dotted := strings.Join(fieldPath, ".")
		m[strings.ToLower(strings.ReplaceAll(dotted, ".", "_"))] = dotted
	}
}

// newEnvKeyMapper returns an env.Provider transform for a config struct
// shaped like cfg. configEnvAliases are checked first (top-level shortcuts
// outside the struct); prefixAliases next, for a legacy prefix that renames
// wholesale into a real section (e.g. rack.go's old "bus_" env vars into
// "snake."), preserving whatever remains of the key unmangled; then the
// struct's own shape; unmatched keys fall back to the old flat lower-cased,
// dotted form; so a genuine typo still fails by matching nothing rather
// than by panicking.
func newEnvKeyMapper(cfg any, prefixAliases map[string]string) func(string) string {
	keyMap := buildEnvKeyMap(cfg)
	return func(s string) string {
		s = strings.TrimPrefix(s, "FLUXRIG_")
		s = strings.ToLower(s)

		if real, ok := configEnvAliases[s]; ok {
			return real
		}
		for prefix, section := range prefixAliases {
			if strings.HasPrefix(s, prefix) {
				return section + "." + strings.TrimPrefix(s, prefix)
			}
		}
		if real, ok := keyMap[s]; ok {
			return real
		}
		return strings.ReplaceAll(s, "_", ".")
	}
}
