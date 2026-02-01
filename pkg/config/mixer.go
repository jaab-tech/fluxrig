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

// MixerConfig defines the startup configuration for the Mixer.
type MixerConfig struct {
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
	// MachineID is the unique physical ID of the Mixer machine.
	MachineID uint16 `koanf:"machine_id" example:"1"`
	// MixerName is the human-readable name of the Mixer.
	MixerName string `koanf:"mixer_name" example:"mixer-01"`
	// StartupScenario path to a scenario YAML file to load on startup.
	StartupScenario string `koanf:"startup_scenario"`
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

// SnakeConfig settings for the embedded NATS server.
type SnakeConfig struct {
	// Port for NATS client connections.
	Port int `koanf:"port" example:"4222"`
	// URL for internal connections.
	URL string `koanf:"url" example:"nats://localhost:4222"`
	// ClusterName for NATS clustering.
	ClusterName string `koanf:"cluster_name" example:"flux"`
	// StreamName for the primary business stream.
	StreamName string `koanf:"stream_name" example:"flux-msg"`
	// StreamSubjects to bind to the stream.
	StreamSubjects []string `koanf:"stream_subjects" example:"flux.msg.>"`
	// Durable toggles file-based storage.
	Durable bool `koanf:"durable" example:"false"`
	// OperationTimeout for NATS requests.
	OperationTimeout string `koanf:"operation_timeout" example:"5s"`
	// BusinessStreamMaxAge retention policy.
	BusinessStreamMaxAge string `koanf:"business_stream_max_age" example:"720h"`
	// TelemetryStreamMaxAge retention policy.
	TelemetryStreamMaxAge string `koanf:"telemetry_stream_max_age" example:"24h"`
	// TLSCertFile for server-side TLS.
	TLSCertFile string `koanf:"tls_cert_file"`
	// TLSKeyFile for server-side TLS.
	TLSKeyFile string `koanf:"tls_key_file"`
	// RootCAFile for client connections (loopback).
	RootCAFile string `koanf:"root_ca_file"`
}

// StoreConfig is defined in rack.go (shared package config)

// LoadMixer reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadMixer(path string) (*MixerConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/mixer.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)

	// Defaults: Store
	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)
	_ = k.Set("store.database_file", "fluxrig.duckdb")
	_ = k.Set("store.cluster_key_file", "cluster.key")

	_ = k.Set("mixer.machine_id", 1)
	_ = k.Set("api.port", 8090)
	_ = k.Set("api.read_header_timeout", "3s")
	_ = k.Set("snake.port", 4222)
	_ = k.Set("snake.url", "nats://localhost:4222")

	// Defaults: Telemetry (Self-Monitoring)
	_ = k.Set("telemetry.service_name", "flux-mixer")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)
	_ = k.Set("snake.cluster_name", "flux")
	_ = k.Set("snake.stream_name", "")
	_ = k.Set("snake.stream_subjects", []string{})
	_ = k.Set("snake.durable", false)
	_ = k.Set("snake.operation_timeout", "5s")
	_ = k.Set("snake.business_stream_max_age", "720h") // 30 days
	_ = k.Set("snake.telemetry_stream_max_age", "24h")

	_ = k.Set("ingest.flush_interval", "5s")
	_ = k.Set("ingest.buffer_size", 1024)

	_ = k.Set("observability.tier", "embedded")
	// observability.embedded.data_dir removed
	_ = k.Set("observability.embedded.flush_interval", "5s")
	_ = k.Set("observability.embedded.retention_days", 30)
	_ = k.Set("enrollment.push_delay", "1s")

	// 2. File (if provided)
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			// If file is explicitly provided but fails, return error
			return nil, err
		}
	}

	// 3. Environment Variables
	// FLUXRIG_API_PORT -> api.port
	err := k.Load(env.Provider("FLUXRIG_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "FLUXRIG_")
		s = strings.ToLower(s)
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

	return &cfg, nil
}
