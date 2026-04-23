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

// RackConfig defines the startup configuration for a Rack instance.
type RackConfig struct {
	Logging   LoggingConfig   `koanf:"logging"`
	Store     StoreConfig     `koanf:"store"`
	Rack      RackSettings    `koanf:"rack"`
	Telemetry TelemetryConfig `koanf:"telemetry"`
}

// TelemetryConfig configures self-reporting metrics and logs.
type TelemetryConfig struct {
	ServiceName   string        `koanf:"service_name"`
	BatchInterval string        `koanf:"batch_interval"`
	BaseSubject   string        `koanf:"base_subject"`
	MaxBatchSize  int           `koanf:"max_batch_size"`
	Metrics       MetricsConfig `koanf:"metrics"`
}

type MetricsConfig struct {
	HostEnabled    bool `koanf:"host_enabled"`
	RuntimeEnabled bool `koanf:"runtime_enabled"`
	BentoEnabled   bool `koanf:"bento_enabled"`
}

// LoggingConfig configures the local text logger.
type LoggingConfig struct {
	Level      string           `koanf:"level"`
	Filename   string           `koanf:"filename"`
	MaxSizeMB  int              `koanf:"max_size_mb"`
	MaxBackups int              `koanf:"max_backups"`
	Compress   bool             `koanf:"compress"`
	Throttling ThrottlingConfig `koanf:"throttling"`
}

// StoreConfig configures persistent storage locations.
type StoreConfig struct {
	Dir            string `koanf:"dir"`
	WALMaxSizeMB   int    `koanf:"wal_max_size_mb"`
	StateFile      string `koanf:"state_file"`
	DatabaseFile   string `koanf:"database_file"`
	ClusterKeyFile string `koanf:"cluster_key_file"`
}

// ThrottlingConfig limits log volume.
type ThrottlingConfig struct {
	Enabled bool    `koanf:"enabled"`
	Rate    float64 `koanf:"rate"`
	Burst   int     `koanf:"burst"`
}

// RackSettings defines identity and lifecycle timeouts.
type RackSettings struct {
	Name               string    `koanf:"name"`
	NamePrefix         string    `koanf:"name_prefix"`
	MachineID          uint16    `koanf:"machine_id"`
	ConvergenceTimeout string    `koanf:"convergence_timeout"`
	HandshakeInterval  string    `koanf:"handshake_interval"`
	HeartbeatInterval  string    `koanf:"heartbeat_interval"`
	CleanupTimeout     string    `koanf:"cleanup_timeout"`
	EnrollmentTimeout  string    `koanf:"enrollment_timeout"`
	Bus                BusConfig `koanf:"bus"`
}

// BusConfig configures the NATS client connection.
type BusConfig struct {
	URL                       string `koanf:"url"`
	StreamName                string `koanf:"stream_name"`
	Domain                    string `koanf:"domain"`
	ConnectTimeout            string `koanf:"connect_timeout"`
	ReconnectWait             string `koanf:"reconnect_wait"`
	OperationTimeout          string `koanf:"operation_timeout"`
	SubscriptionRetryWait     string `koanf:"subscription_retry_wait"`
	SubscriptionRetryAttempts int    `koanf:"subscription_retry_attempts"`
	ConvergenceDelay          string `koanf:"convergence_delay"`
	RootCA                    string `koanf:"root_ca"`
	InsecureSkipVerify        bool   `koanf:"insecure_skip_verify"`
}

// LoadRack reads configuration from a TOML file and Environment Variables.
func LoadRack(path string) (*RackConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/fluxrig.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)
	_ = k.Set("logging.throttling.enabled", true)
	_ = k.Set("logging.throttling.rate", 500.0)
	_ = k.Set("logging.throttling.burst", 50)

	_ = k.Set("telemetry.service_name", "flux-rack")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)

	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)
	_ = k.Set("store.state_file", "state.flux")

	_ = k.Set("rack.name_prefix", "node-")
	_ = k.Set("rack.machine_id", 0)
	_ = k.Set("rack.cleanup_timeout", "2s")
	_ = k.Set("rack.convergence_timeout", "5s")
	_ = k.Set("rack.handshake_interval", "500ms")
	_ = k.Set("rack.heartbeat_interval", "30s")
	_ = k.Set("rack.enrollment_timeout", "2s")

	_ = k.Set("rack.bus.url", "nats://localhost:4222")
	_ = k.Set("rack.bus.domain", "flux")
	_ = k.Set("rack.bus.stream_name", "flux-msg")
	_ = k.Set("rack.bus.connect_timeout", "10s")
	_ = k.Set("rack.bus.reconnect_wait", "1s")
	_ = k.Set("rack.bus.operation_timeout", "5s")
	_ = k.Set("rack.bus.subscription_retry_wait", "200ms")
	_ = k.Set("rack.bus.subscription_retry_attempts", 5)
	_ = k.Set("rack.bus.convergence_delay", "100ms")

	// 2. Load from File
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// 3. Environment Variables (FLUX_ prefix)
	err := k.Load(env.Provider("FLUXRIG_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "FLUXRIG_")
		s = strings.ToLower(s)

		if strings.HasPrefix(s, "rack_bus_") {
			return strings.Replace(s, "rack_bus_", "rack.bus.", 1)
		}
		if strings.HasPrefix(s, "rack_") {
			return strings.Replace(s, "rack_", "rack.", 1)
		}
		if strings.HasPrefix(s, "logging_") {
			return strings.Replace(s, "logging_", "logging.", 1)
		}
		return strings.Replace(s, "_", ".", -1)
	}), nil)

	if err != nil {
		return nil, err
	}

	var cfg RackConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
