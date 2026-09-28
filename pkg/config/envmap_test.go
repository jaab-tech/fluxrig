// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// A hand-maintained prefix list drifts silently: rack.go had no "telemetry_"
// entry, so every multi-word telemetry.* field's environment variable
// matched nothing, and mixer.go had no per-section handling at all, so
// every multi-word field anywhere was broken this way. Spot-checking a few
// keys does not catch that; only exercising every leaf field does. This
// walks each config struct's own koanf tags, sets a distinct value per leaf
// field through its environment variable, loads the config, and confirms
// the value landed at the right path.
func TestEnvKeyMap_MixerConfig_EveryLeafFieldRoundTrips(t *testing.T) {
	assertEveryLeafFieldRoundTrips(t, MixerConfig{}, func() (any, error) { return LoadMixer("") })
}

func TestEnvKeyMap_RackConfig_EveryLeafFieldRoundTrips(t *testing.T) {
	assertEveryLeafFieldRoundTrips(t, RackConfig{}, func() (any, error) { return LoadRack("") })
}

type configLeaf struct {
	envVar string
	path   []string
	typ    reflect.Type
}

func discoverConfigLeaves(rt reflect.Type) []configLeaf {
	var leaves []configLeaf
	var walk func(rt reflect.Type, path []string)
	walk = func(rt reflect.Type, path []string) {
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
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
				walk(ft, fieldPath)
				continue
			}
			leaves = append(leaves, configLeaf{
				envVar: "FLUXRIG_" + strings.ToUpper(strings.Join(fieldPath, "_")),
				path:   fieldPath,
				typ:    f.Type,
			})
		}
	}
	walk(rt, nil)
	return leaves
}

func fieldByKoanfTag(t *testing.T, v reflect.Value, tag string) reflect.Value {
	t.Helper()
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Tag.Get("koanf") == tag {
			return v.Field(i)
		}
	}
	t.Fatalf("no field with koanf tag %q in %s", tag, rt)
	return reflect.Value{}
}

func assertEveryLeafFieldRoundTrips(t *testing.T, zero any, load func() (any, error)) {
	t.Helper()

	leaves := discoverConfigLeaves(reflect.TypeOf(zero))
	if len(leaves) == 0 {
		t.Fatal("no leaf fields discovered - the walker is broken, not the config")
	}

	want := make(map[string]string, len(leaves))
	for i, l := range leaves {
		var val string
		switch l.typ.Kind() {
		case reflect.Bool:
			val = "true"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			val = strconv.Itoa(90000 + i)
		case reflect.Float64:
			val = fmt.Sprintf("%d.5", 90000+i)
		case reflect.Slice:
			val = fmt.Sprintf("sentinel-%d-a,sentinel-%d-b", i, i)
		default:
			val = fmt.Sprintf("sentinel-%d", i)
		}
		want[strings.Join(l.path, ".")] = val
		t.Setenv(l.envVar, val)
	}

	cfgAny, err := load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	cfgVal := reflect.ValueOf(cfgAny)
	if cfgVal.Kind() == reflect.Pointer {
		cfgVal = cfgVal.Elem()
	}

	for _, l := range leaves {
		fv := cfgVal
		for _, tag := range l.path {
			fv = fieldByKoanfTag(t, fv, tag)
		}

		var got string
		if l.typ.Kind() == reflect.Slice {
			parts := make([]string, fv.Len())
			for i := 0; i < fv.Len(); i++ {
				parts[i] = fmt.Sprintf("%v", fv.Index(i).Interface())
			}
			got = strings.Join(parts, ",")
		} else {
			got = fmt.Sprintf("%v", fv.Interface())
		}

		path := strings.Join(l.path, ".")
		if got != want[path] {
			t.Errorf("%s (koanf path %q) did not round-trip: want %q, got %q", l.envVar, path, want[path], got)
		}
	}
}
