// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
)

// durationSuffixes are the koanf-tag endings that mark a string field as a
// duration. A string field whose tag ends in one of these is parsed with
// time.ParseDuration at load time, so a typo fails loudly instead of reaching
// a consumer that discards the parse error and treats the zero value as real.
var durationSuffixes = []string{
	"_timeout",
	"_interval",
	"_wait",
	"_delay",
	"_age",
	"_duration",
	"_ttl",
}

// knownAliasPrefixes lists the exact dotted prefixes a file may set that are
// not struct fields, because a loader reads them by hand: the Rack's legacy
// rack.bus.* alias, and the Mixer's mixer.mixer_name. A whole section is not
// listed, only the paths that are actually read, so a dead key such as
// rack.telemetry.enabled is still caught.
var knownAliasPrefixes = []string{
	"rack.bus.",
	"rack.name",
	"mixer.mixer_name",
	"store.state_file",
	"store.scenario_file",
}

// validateDurations walks v's struct tree and parses every non-empty string
// field whose koanf tag marks it as a duration. It returns an error naming the
// first field that does not parse.
func validateDurations(v any) error {
	return walkDurations(reflect.ValueOf(v), nil)
}

func walkDurations(rv reflect.Value, path []string) error {
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}

	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("koanf")
		if tag == "" || tag == "-" {
			continue
		}
		fieldPath := append(append([]string{}, path...), tag)
		fv := rv.Field(i)

		switch fv.Kind() {
		case reflect.Struct:
			if err := walkDurations(fv, fieldPath); err != nil {
				return err
			}
		case reflect.String:
			if !isDurationField(tag) {
				continue
			}
			raw := fv.String()
			if raw == "" {
				continue
			}
			if _, err := time.ParseDuration(raw); err != nil {
				return fmt.Errorf("invalid duration for %s: %q: %w", strings.Join(fieldPath, "."), raw, err)
			}
		}
	}
	return nil
}

func isDurationField(tag string) bool {
	for _, suffix := range durationSuffixes {
		if strings.HasSuffix(tag, suffix) {
			return true
		}
	}
	return false
}

// validateKnownKeys rejects a key present in k that maps to no field of v's
// struct tree (and is not a known top-level alias section). This is what turns
// a silently ignored override, a typo'd section or field, into a startup error.
//
// Call this on the keys that came from the configuration file, never on the
// merged instance: the FLUXRIG_ environment namespace also carries process
// control variables that are not configuration at all (FLUXRIG_BIN_DIR,
// FLUXRIG_E2E_SKIP_BUILD, FLUXRIG_ROBOT_SKIP_BIN_CHECK and the like), and
// rejecting those would make the binaries refuse to start under the test
// harness and under any operator who exports one.
func validateKnownKeys(k *koanf.Koanf, v any) error {
	known := make(map[string]bool)
	collectKoanfPaths(reflect.TypeOf(v), nil, known)

	var unknown []string
	for _, key := range k.Keys() {
		if known[key] {
			continue
		}
		// An intermediate key (a section, such as "snake") is not a leaf and
		// is not in the set; accept it when a known key sits under it.
		if isPrefixOfKnown(key, known) {
			continue
		}
		// A path the loader reads by hand, not through a struct field, is not
		// in the walk's set.
		if isKnownAlias(key) {
			continue
		}
		unknown = append(unknown, key)
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown configuration key(s): %s", strings.Join(unknown, ", "))
	}
	return nil
}

// isKnownAlias reports whether key is one of the hand-read alias paths. A
// prefix entry ending in "." matches the path itself and everything under it.
func isKnownAlias(key string) bool {
	for _, prefix := range knownAliasPrefixes {
		if strings.HasSuffix(prefix, ".") {
			if strings.HasPrefix(key, prefix) {
				return true
			}
			continue
		}
		if key == prefix {
			return true
		}
	}
	return false
}

// isPrefixOfKnown reports whether key is an ancestor section of a known leaf
// (for example "snake" when "snake.url" is known).
func isPrefixOfKnown(key string, known map[string]bool) bool {
	prefix := key + "."
	for k := range known {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// collectKoanfPaths records the dotted koanf path of every leaf field in t.
func collectKoanfPaths(t reflect.Type, path []string, out map[string]bool) {
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
			collectKoanfPaths(ft, fieldPath, out)
			continue
		}
		out[strings.Join(fieldPath, ".")] = true
	}
}
