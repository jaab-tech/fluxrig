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
	if cfg.Store.DatabaseFile != "fluxrig.duckdb" {
		t.Errorf("Default store database file mismatch: got %s", cfg.Store.DatabaseFile)
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
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
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
	os.Setenv("FLUXRIG_LOGGING_LEVEL", "debug")
	os.Setenv("FLUXRIG_API_PORT", "7777")
	defer os.Unsetenv("FLUXRIG_LOGGING_LEVEL")
	defer os.Unsetenv("FLUXRIG_API_PORT")

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
