// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// RackConfig defines the startup configuration for a Rack instance.
// Reference: ops/docs/internal/implementation.md
type RackConfig struct {
	Logging   LoggingConfig   `koanf:"logging"`
	Store     StoreConfig     `koanf:"store"`
	Rack      RackSettings    `koanf:"rack"`
	Telemetry TelemetryConfig `koanf:"telemetry"`
}

// TelemetryConfig configures self-reporting metrics and logs.
type TelemetryConfig struct {
	// ServiceName as reported in traces.
	ServiceName string `koanf:"service_name" example:"flux-mixer"`
	// BatchInterval for OTel exports.
	BatchInterval string `koanf:"batch_interval" example:"5s"`
	// BaseSubject for telemetry NATS messages.
	BaseSubject string `koanf:"base_subject" example:"flux.telemetry"`
	// MaxBatchSize items per export.
	MaxBatchSize int `koanf:"max_batch_size" example:"512"`
	// Metrics configuration for granular control.
	Metrics MetricsConfig `koanf:"metrics"`
}

type MetricsConfig struct {
	HostEnabled    bool `koanf:"host_enabled"`
	RuntimeEnabled bool `koanf:"runtime_enabled"`
	BentoEnabled   bool `koanf:"bento_enabled"`
}

// LoggingConfig configures the local text logger.
type LoggingConfig struct {
	// Level of logging (debug, info, warn, error).
	Level string `koanf:"level" example:"info"`
	// Filename relative to log root.
	Filename string `koanf:"filename" example:"logs/fluxrig.log"`
	// MaxSizeMB before rotation.
	MaxSizeMB int `koanf:"max_size_mb" example:"100"`
	// MaxBackups kept.
	MaxBackups int `koanf:"max_backups" example:"7"`
	// Compress rotated logs.
	Compress bool `koanf:"compress" example:"true"`
	// Throttling configuration for logs.
	Throttling ThrottlingConfig `koanf:"throttling"`
}

// StoreConfig configures persistent storage locations.
type StoreConfig struct {
	// Dir is the root directory for data storage.
	Dir string `koanf:"dir" example:"./data"`
	// WalMaxSizeMB is the safe limit for write-ahead log growth.
	WALMaxSizeMB int `koanf:"wal_max_size_mb" example:"500"`
	// StateFile is the filename for the Rack's state envelope.
	StateFile string `koanf:"state_file" example:"state.flux"`
	// DatabaseFile is the DuckDB filename.
	DatabaseFile string `koanf:"database_file" example:"fluxrig.duckdb"`
	// ClusterKeyFile is the filename for the public cluster key.
	ClusterKeyFile string `koanf:"cluster_key_file" example:"cluster.key"`
}

// ThrottlingConfig limits log volume.
type ThrottlingConfig struct {
	// Enabled toggles log throttling.
	Enabled bool `koanf:"enabled" example:"true"`
	// Rate of allowed log lines per second.
	Rate float64 `koanf:"rate" example:"100.0"`
	// Burst capacity.
	Burst int `koanf:"burst" example:"10"`
}

// RackSettings defines the identity of this Rack instance.
type RackSettings struct {
	// Name of the rack (unique in the cluster).
	Name string `koanf:"name" example:"rack-01"`
	// NamePrefix for auto-generated names.
	NamePrefix string `koanf:"name_prefix" example:"node-"`
	// MachineID is the unique physical ID (if assigned).
	MachineID uint16 `koanf:"machine_id" example:"10"`
	// HeartbeatInterval for sending status updates.
	HeartbeatInterval string `koanf:"heartbeat_interval" example:"30s"`
	// EnrollmentTimeout for waiting for adoption.
	EnrollmentTimeout string `koanf:"enrollment_timeout" example:"2s"`
	// Bus configuration for NATS connection.
	Bus BusConfig `koanf:"bus"`
}

// BusConfig configures the NATS client connection.
type BusConfig struct {
	// URL of the NATS server.
	URL string `koanf:"url" example:"nats://localhost:4222"`
	// StreamName for business logic.
	StreamName string `koanf:"stream_name" example:"flux-msg"`
	// ConnectTimeout for initial connection.
	ConnectTimeout string `koanf:"connect_timeout" example:"10s"`
	// ReconnectWait between attempts.
	ReconnectWait string `koanf:"reconnect_wait" example:"1s"`
	// OperationTimeout for pub/sub operations.
	OperationTimeout string `koanf:"operation_timeout" example:"5s"`
	// RootCA path to the trusted root certificate (for self-signed certs).
	RootCA string `koanf:"root_ca" example:"ca.crt"`
}

// LoadRack reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadRack(path string) (*RackConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/fluxrig.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)

	// Defaults: Telemetry
	_ = k.Set("telemetry.service_name", "flux-rack")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)

	// Defaults: Throttling (QoS)
	_ = k.Set("logging.throttling.enabled", true)
	_ = k.Set("logging.throttling.rate", 500.0)
	_ = k.Set("logging.throttling.burst", 50)

	// Defaults: Store
	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)
	_ = k.Set("store.state_file", "state.flux")

	_ = k.Set("rack.bus.url", "nats://localhost:4222")
	_ = k.Set("rack.name_prefix", "node-")
	_ = k.Set("rack.machine_id", 0)
	_ = k.Set("rack.heartbeat_interval", "30s")
	_ = k.Set("rack.enrollment_timeout", "2s")
	_ = k.Set("rack.bus.connect_timeout", "10s")
	_ = k.Set("rack.bus.connect_timeout", "10s")
	_ = k.Set("rack.bus.reconnect_wait", "1s")
	_ = k.Set("rack.bus.operation_timeout", "5s")
	// Stream Name defaults to flux-msg (Business)
	_ = k.Set("rack.bus.stream_name", "flux-msg")

	// 2. File (if provided)
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			// If file is optional we might ignore this, but usually explicit path means explicit load.
			// For now, return error if load fails.
			return nil, err
		}
	}

	// 3. Environment Variables
	// Mapped as FLUXRIG_RACK_NAME -> rack.name
	// FLUXRIG_LOGGING_LEVEL -> logging.level
	// Custom Environment Variable Provider
	// Maps FLUXRIG_ structure to nested config keys.
	// Specific overrides are needed for keys containing underscores (e.g., data_dir)
	// which conflict with the standard "_" to "." separator replacement.
	err := k.Load(env.Provider("FLUXRIG_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "FLUXRIG_")
		s = strings.ToLower(s)

		// 1. Handle Known Hierarchies explicitely to preserve internal underscores
		if strings.HasPrefix(s, "rack_bus_") {
			return strings.Replace(s, "rack_bus_", "rack.bus.", 1)
		}
		if strings.HasPrefix(s, "rack_") {
			return strings.Replace(s, "rack_", "rack.", 1)
		}
		if strings.HasPrefix(s, "logging_") {
			return strings.Replace(s, "logging_", "logging.", 1)
		}

		// 2. Default: Replace all underscores with dots
		return strings.Replace(s, "_", ".", -1)
	}), nil)

	if err != nil {
		return nil, err
	}

	// Unmarshal
	var cfg RackConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
