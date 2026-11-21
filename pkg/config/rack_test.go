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
	if cfg.Snake.URL != "nats://localhost:4222" {
		t.Errorf("Default snake URL mismatch: got %s", cfg.Snake.URL)
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
	if cfg.Base.Name != "env-rack" {
		t.Errorf("Env override failed: got %s, want env-rack", cfg.Base.Name)
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
	tmpfile, err := os.CreateTemp("", "flux.*.toml")
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
	if cfg.Base.Name != "file-rack" {
		t.Errorf("File load failed: got %s, want file-rack", cfg.Base.Name)
	}
}

// A Rack moves to another Mixer after two publishes in a row find no
// stream: with the 30s heartbeat that is inside the Mixer's 90s liveness TTL.
func TestLoadRack_FailoverAfter(t *testing.T) {
	cfg, err := LoadRack("")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.Snake.FailoverAfter != 2 {
		t.Errorf("default snake.failover_after: got %d, want 2", cfg.Snake.FailoverAfter)
	}
	cfg, err = LoadRack(writeTempTOML(t, "[snake]\nfailover_after = 0\n"))
	if err != nil || cfg.Snake.FailoverAfter != 0 {
		t.Errorf("0 disables it and must load: %v, %d", err, cfg.Snake.FailoverAfter)
	}
	if _, err := LoadRack(writeTempTOML(t, "[snake]\nfailover_after = -1\n")); err == nil {
		t.Error("a negative snake.failover_after must be refused")
	}
}

func TestLoadRack_ConsumerMissedHeartbeats(t *testing.T) {
	cfg, err := LoadRack("")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.Snake.ConsumerMissedHeartbeats != 2 {
		t.Errorf("default snake.consumer_missed_heartbeats: got %d, want 2", cfg.Snake.ConsumerMissedHeartbeats)
	}
	cfg, err = LoadRack(writeTempTOML(t, "[snake]\nconsumer_missed_heartbeats = 4\n"))
	if err != nil || cfg.Snake.ConsumerMissedHeartbeats != 4 {
		t.Errorf("a configured value must load: %v, %d", err, cfg.Snake.ConsumerMissedHeartbeats)
	}
	if _, err := LoadRack(writeTempTOML(t, "[snake]\nconsumer_missed_heartbeats = 0\n")); err == nil {
		t.Error("snake.consumer_missed_heartbeats below 1 must be refused")
	}
}

func TestLoadRack_Pings(t *testing.T) {
	cfg, err := LoadRack("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Snake.PingInterval != DefaultPingInterval || cfg.Snake.MaxPingsOut != DefaultMaxPingsOut {
		t.Errorf("ping defaults: got %q and %d", cfg.Snake.PingInterval, cfg.Snake.MaxPingsOut)
	}
	for _, bad := range []string{"ping_interval = \"0s\"", "max_pings_out = 0"} {
		if _, err := LoadRack(writeTempTOML(t, "[snake]\n"+bad+"\n")); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestLoadRack_AckWait(t *testing.T) {
	cfg, err := LoadRack("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Snake.AckWait != DefaultAckWait {
		t.Errorf("ack_wait default: got %q", cfg.Snake.AckWait)
	}
	for _, bad := range []string{"ack_wait = \"0s\"", "ack_wait = \"5s\"", "ack_wait = \"3s\""} {
		if _, err := LoadRack(writeTempTOML(t, "[snake]\n"+bad+"\n")); err == nil {
			t.Errorf("%s with operation_timeout 5s: expected an error", bad)
		}
	}
	if _, err := LoadRack(writeTempTOML(t, "[snake]\nack_wait = \"6s\"\n")); err != nil {
		t.Errorf("ack_wait 6s with operation_timeout 5s: %v", err)
	}
}
