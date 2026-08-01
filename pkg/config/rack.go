// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"

	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// RackConfig defines the startup configuration for a Rack instance.
type RackConfig struct {
	Base      BaseConfig      `koanf:"base"`
	Logging   LoggingConfig   `koanf:"logging"`
	Store     StoreConfig     `koanf:"store"`
	Rack      RackSettings    `koanf:"rack"`
	Telemetry TelemetryConfig `koanf:"telemetry"`
	Snake     SnakeConfig     `koanf:"snake"`
}

// RackSettings defines identity and lifecycle timeouts.
type RackSettings struct {
	NamePrefix         string    `koanf:"name_prefix"`
	MachineID          uuid.UUID `koanf:"-"` // Runtime only: set via Passport or Mixer assignment
	IP                 string    `koanf:"ip" example:"10.0.0.5"`
	ConvergenceTimeout string    `koanf:"convergence_timeout"`
	HandshakeInterval  string    `koanf:"handshake_interval"`
	HeartbeatInterval  string    `koanf:"heartbeat_interval"`
	CleanupTimeout     string    `koanf:"cleanup_timeout"`
	// DrainTimeout bounds a graceful stop (SIGTERM or shutdown command). It
	// must exceed the largest gear ticket TTL so in-flight work can complete
	// or time out before the process exits. Default 35s (> a 30s switch TTL).
	DrainTimeout       string `koanf:"drain_timeout"`
	EnrollmentTimeout  string `koanf:"enrollment_timeout"`
	EnrollmentInterval string `koanf:"enrollment_interval"`
	MaxHops            int    `koanf:"max_hops"`
	MaxPayloadSize     int    `koanf:"max_payload_size"`
}

// LoadRack reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults. If path is empty, it returns the default configuration.
func LoadRack(path string) (*RackConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("base.state_dir", "./data")
	_ = k.Set("base.state_file", "rack.flux")

	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/fluxrig.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)
	_ = k.Set("logging.throttling.enabled", true)
	_ = k.Set("logging.throttling.rate", 500.0)
	_ = k.Set("logging.throttling.burst", 50)

	_ = k.Set("telemetry.service_name", "flux.rack")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)
	_ = k.Set("telemetry.stream_name", "flux-telemetry")

	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)

	_ = k.Set("rack.name_prefix", "node-")
	_ = k.Set("rack.cleanup_timeout", "2s")
	_ = k.Set("rack.drain_timeout", "35s")
	_ = k.Set("rack.convergence_timeout", "5s")
	_ = k.Set("rack.handshake_interval", "500ms")
	_ = k.Set("rack.heartbeat_interval", "30s")
	_ = k.Set("rack.enrollment_timeout", "15s")
	_ = k.Set("rack.enrollment_interval", "2s")
	_ = k.Set("rack.max_hops", 64)
	_ = k.Set("rack.max_payload_size", 2*1024*1024) // 2MB

	_ = k.Set("snake.url", "nats://localhost:4222")
	_ = k.Set("snake.domain", "flux")
	_ = k.Set("snake.stream_name", "flux-msg")
	_ = k.Set("snake.connect_timeout", "10s")
	_ = k.Set("snake.reconnect_wait", "1s")
	_ = k.Set("snake.operation_timeout", "5s")
	_ = k.Set("snake.subscription_retry_wait", "200ms")
	_ = k.Set("snake.subscription_retry_attempts", 5)
	_ = k.Set("snake.inactive_threshold", "30s")
	_ = k.Set("snake.convergence_delay", "100ms")

	// 2. Load from File
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// 3. Environment Variables (FLUXRIG_ prefix)
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

		if strings.HasPrefix(s, "snake_") {
			return strings.Replace(s, "snake_", "snake.", 1)
		}
		if strings.HasPrefix(s, "rack_bus_") || strings.HasPrefix(s, "bus_") {
			return "snake." + strings.TrimPrefix(strings.TrimPrefix(s, "rack_bus_"), "bus_")
		}
		if strings.HasPrefix(s, "rack_") {
			return strings.Replace(s, "rack_", "rack.", 1)
		}
		if strings.HasPrefix(s, "logging_") {
			return strings.Replace(s, "logging_", "logging.", 1)
		}
		if strings.HasPrefix(s, "base_") {
			return strings.Replace(s, "base_", "base.", 1)
		}
		if strings.HasPrefix(s, "store_") {
			return strings.Replace(s, "store_", "store.", 1)
		}
		return strings.ReplaceAll(s, "_", ".")
	}), nil)

	if err != nil {
		return nil, err
	}

	// 4. Backward Compatibility: Map rack.bus to snake
	if k.Exists("rack.bus.url") {
		_ = k.Set("snake.url", k.String("rack.bus.url"))
		if k.Exists("rack.bus.domain") {
			_ = k.Set("snake.domain", k.String("rack.bus.domain"))
		}
		if k.Exists("rack.bus.stream_name") {
			_ = k.Set("snake.stream_name", k.String("rack.bus.stream_name"))
		}
		if k.Exists("rack.bus.root_ca_file") {
			_ = k.Set("snake.root_ca_file", k.String("rack.bus.root_ca_file"))
		}
		if k.Exists("rack.bus.insecure_skip_verify") {
			_ = k.Set("snake.insecure_skip_verify", k.Bool("rack.bus.insecure_skip_verify"))
		}
	}

	// Map store.state_file to base.state_file (Always override if store.state_file is present in File/Env)
	if k.Exists("store.state_file") {
		_ = k.Set("base.state_file", k.String("store.state_file"))
	}

	var cfg RackConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// 5. Final fallback for fields that don't map perfectly via koanf tags
	if cfg.Base.Name == "" {
		cfg.Base.Name = k.String("rack.name")
	}
	if cfg.Base.StateDir == "" {
		cfg.Base.StateDir = k.String("store.dir")
	}

	return &cfg, nil
}
