// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"testing"
)

func TestLoadMixer_Defaults(t *testing.T) {
	cfg, err := LoadMixer("")
	if err != nil {
		t.Fatalf("Failed to load defaults: %v", err)
	}

	if cfg.Logging.Level != "info" {
		t.Errorf("Default log level mismatch: got %s", cfg.Logging.Level)
	}
	if cfg.API.Port != 8090 {
		t.Errorf("Default API port mismatch: got %d", cfg.API.Port)
	}
	if cfg.Snake.Port != 4222 {
		t.Errorf("Default Snake port mismatch: got %d", cfg.Snake.Port)
	}
	if cfg.Store.DatabaseFile != "flux.duckdb" {
		t.Errorf("Default store database file mismatch: got %s", cfg.Store.DatabaseFile)
	}
	if cfg.Snake.AllowNonTLS {
		t.Error("AllowNonTLS must default to false: configuring TLS should make it required")
	}
	if cfg.Snake.TLSVerify {
		t.Error("TLSVerify must default to false")
	}
	if cfg.Snake.KVMaxBytes != 1073741824 {
		t.Errorf("Default KVMaxBytes mismatch: got %d", cfg.Snake.KVMaxBytes)
	}
	if cfg.Snake.KVMaxValueSize != 1048576 {
		t.Errorf("Default KVMaxValueSize mismatch: got %d", cfg.Snake.KVMaxValueSize)
	}
	if cfg.Snake.KVTTL != "0s" {
		t.Errorf("Default KVTTL mismatch: got %s", cfg.Snake.KVTTL)
	}
}

// Before this fix, snake.tls_ca_file and snake.tls_verify had no field on
// SnakeConfig at all: the Go TLS logic that reads them existed, but nothing
// in the Mixer's own config could ever populate them.
func TestLoadMixer_SnakeTLSAndKVKeys(t *testing.T) {
	content := `
[snake]
allow_non_tls = true
tls_ca_file = "ca.pem"
tls_verify = true
kv_max_bytes = 2048
kv_max_value_size = 512
kv_ttl = "10m"
`
	tmpfile, err := os.CreateTemp("", "mixer.*.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	if _, errWr := tmpfile.Write([]byte(content)); errWr != nil {
		t.Fatal(errWr)
	}
	if errClose := tmpfile.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	cfg, err := LoadMixer(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to load file: %v", err)
	}

	if !cfg.Snake.AllowNonTLS {
		t.Error("snake.allow_non_tls did not reach SnakeConfig.AllowNonTLS")
	}
	if cfg.Snake.TLSCAFile != "ca.pem" {
		t.Errorf("snake.tls_ca_file did not reach SnakeConfig.TLSCAFile: got %q", cfg.Snake.TLSCAFile)
	}
	if !cfg.Snake.TLSVerify {
		t.Error("snake.tls_verify did not reach SnakeConfig.TLSVerify")
	}
	if cfg.Snake.KVMaxBytes != 2048 {
		t.Errorf("snake.kv_max_bytes did not reach SnakeConfig.KVMaxBytes: got %d", cfg.Snake.KVMaxBytes)
	}
	if cfg.Snake.KVMaxValueSize != 512 {
		t.Errorf("snake.kv_max_value_size did not reach SnakeConfig.KVMaxValueSize: got %d", cfg.Snake.KVMaxValueSize)
	}
	if cfg.Snake.KVTTL != "10m" {
		t.Errorf("snake.kv_ttl did not reach SnakeConfig.KVTTL: got %q", cfg.Snake.KVTTL)
	}
}

func TestLoadMixer_File(t *testing.T) {
	content := `
[logging]
level = "warn"

[api]
port = 9999

[store]
database_file = "custom.duckdb"
cluster_key_file = "custom.key"
`
	tmpfile, err := os.CreateTemp("", "mixer.*.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	if _, errWr := tmpfile.Write([]byte(content)); errWr != nil {
		t.Fatal(errWr)
	}
	if errClose := tmpfile.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	cfg, err := LoadMixer(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to load file: %v", err)
	}

	if cfg.Logging.Level != "warn" {
		t.Errorf("File load failed: got %s, want warn", cfg.Logging.Level)
	}
	if cfg.API.Port != 9999 {
		t.Errorf("File load failed: got %d, want 9999", cfg.API.Port)
	}
	if cfg.Store.DatabaseFile != "custom.duckdb" {
		t.Errorf("File load failed: got %s", cfg.Store.DatabaseFile)
	}
}

func TestLoadMixer_EnvOverride(t *testing.T) {
	_ = os.Setenv("FLUXRIG_LOGGING_LEVEL", "debug")
	_ = os.Setenv("FLUXRIG_API_PORT", "7777")
	defer func() { _ = os.Unsetenv("FLUXRIG_LOGGING_LEVEL") }()
	defer func() { _ = os.Unsetenv("FLUXRIG_API_PORT") }()

	cfg, err := LoadMixer("")
	if err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	if cfg.Logging.Level != "debug" {
		t.Errorf("Env override failed: got %s, want debug", cfg.Logging.Level)
	}
	if cfg.API.Port != 7777 {
		t.Errorf("Env override failed: got %d, want 7777", cfg.API.Port)
	}
}

func TestLoadMixer_FileNotFound(t *testing.T) {
	_, err := LoadMixer("/non/existent/file.toml")
	if err == nil {
		t.Error("Expected error for missing file")
	}
}
