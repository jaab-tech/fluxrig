// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// MixerConfig defines the startup configuration for the Mixer.
type MixerConfig struct {
	Base          BaseConfig          `koanf:"base"`
	Logging       LoggingConfig       `koanf:"logging"`
	Store         StoreConfig         `koanf:"store"`
	Mixer         MixerSettings       `koanf:"mixer"`
	API           ApiConfig           `koanf:"api"`
	Snake         SnakeConfig         `koanf:"snake"`
	Observability ObservabilityConfig `koanf:"observability"`
	Enrollment    EnrollmentConfig    `koanf:"enrollment"`
	Ingest        IngestConfig        `koanf:"ingest"`
	Telemetry     TelemetryConfig     `koanf:"telemetry"`
}

// IngestConfig controls telemetry ingestion buffering and flushing.
type IngestConfig struct {
	// FlushInterval determines how often to flush data to Parquet.
	FlushInterval string `koanf:"flush_interval" example:"5s"`
	// BufferSize is the internal channel buffer size (deprecated).
	BufferSize int `koanf:"buffer_size" example:"1024"`
}

// EnrollmentConfig controls how new Racks are enrolled.
type EnrollmentConfig struct {
	// PushDelay is the time to wait before pushing state to a newly enrolled Rack.
	PushDelay string `koanf:"push_delay" example:"1s"`
	// AutoAdopt, if true, will automatically mark newly enrolled Racks as 'active'.
	// If false (default), new Racks start as 'pending'.
	AutoAdopt bool `koanf:"auto_adopt" example:"false"`
	// BootstrapSecret is the shared secret used for zero-config enrollment and identity adoption.
	// Defaults to 'fluxrig' if not specified.
	BootstrapSecret string `koanf:"bootstrap_secret" example:"fluxrig"`
}

// ObservabilityConfig controls the global observability tier.
type ObservabilityConfig struct {
	// Enabled master switch for observability features.
	Enabled bool `koanf:"enabled" example:"true"`
	// Tier selects the backend (embedded or standard).
	Tier string `koanf:"tier" example:"embedded"`
	// Embedded configures the local embedded stack.
	Embedded EmbeddedConfig `koanf:"embedded"`
}

// EmbeddedConfig settings for the embedded observability stack.
type EmbeddedConfig struct {
	// FlushInterval for the embedded stack.
	FlushInterval string `koanf:"flush_interval" example:"5s"`
	// RetentionDays for local parquet files.
	RetentionDays int `koanf:"retention_days" example:"30"`
}

// MixerSettings defines the identity of this Mixer instance.
type MixerSettings struct {
	// StartupScenario reference to the scenario to load on startup.
	StartupScenario string `koanf:"startup_scenario"`
	// Message Limits
	MaxHops        int `koanf:"max_hops"`
	MaxPayloadSize int `koanf:"max_payload_size"`
	// ScenarioWaitTimeout is the time to wait for a rack to register when activating a scenario.
	ScenarioWaitTimeout string `koanf:"scenario_wait_timeout" example:"15s"`
}

// ApiConfig settings for the Control Plane REST API.
type ApiConfig struct {
	// Port to listen on.
	Port int `koanf:"port" example:"8090"`
	// ReadHeaderTimeout for the HTTP server.
	ReadHeaderTimeout string `koanf:"read_header_timeout" example:"3s"`
	// TLSCertFile path to server certificate.
	TLSCertFile string `koanf:"tls_cert_file" example:"server.crt"`
	// TLSKeyFile path to server key.
	TLSKeyFile string `koanf:"tls_key_file" example:"server.key"`
}

// LoadMixer reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults. If path is empty, it returns the default configuration.
func LoadMixer(path string) (*MixerConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("base.name", "fluxrig-mixer")
	_ = k.Set("base.state_dir", "./data")

	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/mixer.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)

	// Defaults: Store
	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)
	_ = k.Set("store.database_file", "flux.duckdb")
	_ = k.Set("store.cluster_key_file", "cluster.key")

	_ = k.Set("mixer.max_hops", 64)
	_ = k.Set("mixer.max_payload_size", 2*1024*1024) // 2MB
	_ = k.Set("mixer.scenario_wait_timeout", "15s")
	_ = k.Set("api.port", 8090)
	_ = k.Set("api.read_header_timeout", "3s")

	// Defaults: Snake
	_ = k.Set("snake.url", "nats://localhost:4222")
	_ = k.Set("snake.port", 4222)
	_ = k.Set("snake.domain", "flux")
	_ = k.Set("snake.stream_name", "flux-msg")
	_ = k.Set("snake.operation_timeout", "5s")
	_ = k.Set("snake.inactive_threshold", "30s")

	// Defaults: Telemetry (Self-Monitoring)
	_ = k.Set("telemetry.service_name", "flux.mixer")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)
	_ = k.Set("telemetry.stream_name", "flux-telemetry")

	_ = k.Set("ingest.flush_interval", "5s")
	_ = k.Set("ingest.buffer_size", 1024)

	_ = k.Set("observability.tier", "embedded")
	_ = k.Set("observability.embedded.flush_interval", "5s")
	_ = k.Set("observability.embedded.retention_days", 30)
	_ = k.Set("enrollment.push_delay", "1s")
	_ = k.Set("enrollment.auto_adopt", false)
	_ = k.Set("enrollment.bootstrap_secret", "fluxrig")

	// 2. File (if provided)
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// 3. Environment Variables
	err := k.Load(env.Provider("FLUXRIG_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "FLUXRIG_")
		s = strings.ToLower(s)

		if s == "trace" {
			return "logging.trace"
		}
		if s == "debug" {
			return "logging.debug"
		}
		if s == "disable_telemetry" {
			return "telemetry.disabled"
		}

		// Map BUS to SNAKE for consistency if needed, but here we just use snake.
		s = strings.Replace(s, "_", ".", -1)
		return s
	}), nil)

	if err != nil {
		return nil, err
	}

	var cfg MixerConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Backward Compatibility: Fallback to old field names
	if cfg.Base.Name == "" {
		cfg.Base.Name = k.String("mixer.mixer_name")
	}
	if cfg.Base.StateDir == "" {
		cfg.Base.StateDir = k.String("store.dir")
	}

	return &cfg, nil
}
