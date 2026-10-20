// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestLoadMixer_RegistryDefaults(t *testing.T) {
	cfg, err := LoadMixer("")
	if err != nil {
		t.Fatal(err)
	}
	want := RegistryConfig{
		Bucket: "fluxrig_registry", LivenessBucket: "fluxrig_liveness", Replicas: 1,
		MaxValueSize: 1 << 20, LivenessTTL: "90s", WriteRetries: 3,
		SweepInterval: "10m", OrphanMinAge: "5m",
	}
	if cfg.Registry != want {
		t.Errorf("registry defaults: got %+v, want %+v", cfg.Registry, want)
	}
}

// Zero retries is a setting (fail on the first conflict), not an unset
// field: neither the loader nor WithDefaults may turn it into the default.
func TestLoadMixer_RegistryZeroRetriesKept(t *testing.T) {
	cfg, err := LoadMixer(writeTempTOML(t, "[registry]\nwrite_retries = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Registry.WithDefaults().WriteRetries; got != 0 {
		t.Errorf("write_retries = 0 became %d", got)
	}
}

func TestLoadMixer_RegistryRejectsBadSettings(t *testing.T) {
	for name, body := range map[string]string{
		"replicas":        "replicas = 0",
		"max_value_size":  "max_value_size = 0",
		"write_retries":   "write_retries = -1",
		"liveness_ttl":    `liveness_ttl = "0s"`,
		"same bucket":     `liveness_bucket = "fluxrig_registry"`,
		"value too large": "max_value_size = 4294967296",
		"sweep_interval":  `sweep_interval = "0s"`,
		"orphan_min_age":  `orphan_min_age = "0s"`,
	} {
		_, err := LoadMixer(writeTempTOML(t, "[registry]\n"+body+"\n"))
		if err == nil || !strings.Contains(err.Error(), "registry.") {
			t.Errorf("%s: want a registry.* error, got %v", name, err)
		}
	}
}
