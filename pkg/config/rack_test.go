// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"testing"
)

func TestLoadRack_Defaults(t *testing.T) {
	// 1. Load without file or env
	cfg, err := LoadRack("")
	if err != nil {
		t.Fatalf("Failed to load defaults: %v", err)
	}

	// Check Defaults
	if cfg.Logging.Level != "info" {
		t.Errorf("Default log level mismatch: got %s", cfg.Logging.Level)
	}
	if cfg.Rack.Bus.URL != "nats://localhost:4222" {
		t.Errorf("Default bus URL mismatch: got %s", cfg.Rack.Bus.URL)
	}
}

func TestLoadRack_EnvOverride(t *testing.T) {
	// Set Env
	_ = os.Setenv("FLUXRIG_LOGGING_LEVEL", "debug")
	_ = os.Setenv("FLUXRIG_RACK_NAME", "env-rack")
	defer func() { _ = os.Unsetenv("FLUXRIG_LOGGING_LEVEL") }()
	defer func() { _ = os.Unsetenv("FLUXRIG_RACK_NAME") }()

	cfg, err := LoadRack("")
	if err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	if cfg.Logging.Level != "debug" {
		t.Errorf("Env override failed: got %s, want debug", cfg.Logging.Level)
	}
	if cfg.Rack.Name != "env-rack" {
		t.Errorf("Env override failed: got %s, want env-rack", cfg.Rack.Name)
	}
}

func TestLoadRack_File(t *testing.T) {
	// Create temp file
	content := `
[logging]
level = "warn"

[rack]
name = "file-rack"
`
	tmpfile, err := os.CreateTemp("", "fluxrig.*.toml")
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

	cfg, err := LoadRack(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to load file: %v", err)
	}

	if cfg.Logging.Level != "warn" {
		t.Errorf("File load failed: got %s, want warn", cfg.Logging.Level)
	}
	if cfg.Rack.Name != "file-rack" {
		t.Errorf("File load failed: got %s, want file-rack", cfg.Rack.Name)
	}
}
