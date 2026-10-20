// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write temp toml: %v", err)
	}
	return path
}

func TestLoadMixer_RejectsBadDuration(t *testing.T) {
	path := writeTempTOML(t, `
[snake]
operation_timeout = "5 seconds"
`)
	_, err := LoadMixer(path)
	if err == nil {
		t.Fatal("a duration that does not parse must fail the load, not silently become zero")
	}
	if !strings.Contains(err.Error(), "snake.operation_timeout") {
		t.Errorf("the error must name the field; got: %v", err)
	}
}

func TestLoadRack_RejectsBadDuration(t *testing.T) {
	path := writeTempTOML(t, `
[rack]
heartbeat_interval = "30 secs"
`)
	_, err := LoadRack(path)
	if err == nil {
		t.Fatal("a duration that does not parse must fail the load")
	}
	if !strings.Contains(err.Error(), "rack.heartbeat_interval") {
		t.Errorf("the error must name the field; got: %v", err)
	}
}

func TestLoadMixer_RejectsUnknownKey(t *testing.T) {
	path := writeTempTOML(t, `
[observabilitty]
enabled = true
`)
	_, err := LoadMixer(path)
	if err == nil {
		t.Fatal("a key that maps to no field must fail the load, not be silently ignored")
	}
	if !strings.Contains(err.Error(), "observabilitty.enabled") {
		t.Errorf("the error must name the unknown key; got: %v", err)
	}
}

func TestLoadMixer_RejectsUnknownFieldInKnownSection(t *testing.T) {
	path := writeTempTOML(t, `
[snake]
operation_timout = "5s"
`)
	_, err := LoadMixer(path)
	if err == nil {
		t.Fatal("a misspelled field must fail the load")
	}
	if !strings.Contains(err.Error(), "snake.operation_timout") {
		t.Errorf("the error must name the unknown key; got: %v", err)
	}
}

func TestLoadMixer_AcceptsValidDurationAndKnownKeys(t *testing.T) {
	path := writeTempTOML(t, `
[logging]
level = "debug"

[snake]
operation_timeout = "7s"
stream_max_age = "48h"

[observability.embedded]
retention_days = 14

[observability.embedded.rotation]
check_interval = "30m"
`)
	cfg, err := LoadMixer(path)
	if err != nil {
		t.Fatalf("a valid config must load: %v", err)
	}
	if cfg.Snake.OperationTimeout != "7s" {
		t.Errorf("OperationTimeout mismatch: got %q", cfg.Snake.OperationTimeout)
	}
	if cfg.Observability.Embedded.Rotation.CheckInterval != "30m" {
		t.Errorf("CheckInterval mismatch: got %q", cfg.Observability.Embedded.Rotation.CheckInterval)
	}
}

// The Rack's legacy aliases are read by the loader by hand and map onto a real
// section, so the unknown-key check must not reject them.
func TestLoadRack_AcceptsLegacyAliases(t *testing.T) {
	path := writeTempTOML(t, `
[rack.bus]
url = "nats://legacy:4222"
domain = "legacy-domain"
`)
	cfg, err := LoadRack(path)
	if err != nil {
		t.Fatalf("legacy alias must still load: %v", err)
	}
	if cfg.Snake.URL != "nats://legacy:4222" {
		t.Errorf("rack.bus.url did not map to snake.url: got %q", cfg.Snake.URL)
	}
	if cfg.Snake.Domain != "legacy-domain" {
		t.Errorf("rack.bus.domain did not map to snake.domain: got %q", cfg.Snake.Domain)
	}
}

// Both binaries' default configurations must pass their own validation. This is
// the regression that proves the known-key walk does not flag the defaults the
// loaders themselves set.
func TestDefaults_PassValidation(t *testing.T) {
	if _, err := LoadMixer(""); err != nil {
		t.Errorf("default Mixer config must validate: %v", err)
	}
	if _, err := LoadRack(""); err != nil {
		t.Errorf("default Rack config must validate: %v", err)
	}
}

// The FLUXRIG_ environment namespace also carries process control variables
// that are not configuration at all. A strict unknown-key check must apply to
// the file the operator wrote, never to the merged set, or the binaries refuse
// to start under the test harness (FLUXRIG_BIN_DIR, FLUXRIG_E2E_SKIP_BUILD,
// FLUXRIG_ROBOT_SKIP_BIN_CHECK) and under any operator who exports one.
func TestLoadMixer_EnvControlVarsAreNotRejected(t *testing.T) {
	t.Setenv("FLUXRIG_E2E_SKIP_BUILD", "1")
	t.Setenv("FLUXRIG_BIN_DIR", "/tmp/bin")
	t.Setenv("FLUXRIG_ROBOT_SKIP_BIN_CHECK", "1")
	t.Setenv("FLUXRIG_API_AUTH_DISABLED_DANGEROUSLY", "true")

	path := writeTempTOML(t, `
[snake]
operation_timeout = "5s"
`)
	if _, err := LoadMixer(path); err != nil {
		t.Fatalf("env control variables must not fail the load: %v", err)
	}
}

func TestValidateDurations_EmptyIsAccepted(t *testing.T) {
	cfg := &SnakeConfig{}
	if err := validateDurations(cfg); err != nil {
		t.Errorf("empty duration fields must be accepted: %v", err)
	}
}

// snake.cluster_name and snake.store_dir were fields of the Snake server's own
// configuration with no key on SnakeConfig, so setting them had no effect. This
// pins that a TOML file now reaches them.
func TestLoadMixer_SnakeClusterNameAndStoreDir(t *testing.T) {
	path := writeTempTOML(t, `
[snake]
cluster_name = "prod-cluster"
store_dir = "/srv/fluxrig/snake"
`)
	cfg, err := LoadMixer(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Snake.ClusterName != "prod-cluster" {
		t.Errorf("ClusterName mismatch: got %q", cfg.Snake.ClusterName)
	}
	if cfg.Snake.StoreDir != "/srv/fluxrig/snake" {
		t.Errorf("StoreDir mismatch: got %q", cfg.Snake.StoreDir)
	}
}
