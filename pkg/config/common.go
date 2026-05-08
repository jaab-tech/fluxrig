// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

// BaseConfig defines foundational identity and persistence paths.
type BaseConfig struct {
	Name      string `koanf:"name" example:"node-01"`
	StateDir  string `koanf:"state_dir" example:"./data"`
	StateFile string `koanf:"state_file" example:"rack.flux"`
}

// LoggingConfig configures the structured logger.
type LoggingConfig struct {
	Level      string           `koanf:"level" example:"info"`
	Filename   string           `koanf:"filename" example:"logs/fluxrig.log"`
	MaxSizeMB  int              `koanf:"max_size_mb" example:"100"`
	MaxBackups int              `koanf:"max_backups" example:"7"`
	Compress   bool             `koanf:"compress" example:"true"`
	Trace      bool             `koanf:"trace"` // Hard-override for trace level
	Debug      bool             `koanf:"debug"` // Hard-override for debug level
	Throttling ThrottlingConfig `koanf:"throttling"`
}

// ThrottlingConfig limits log volume to prevent starvation.
type ThrottlingConfig struct {
	Enabled bool    `koanf:"enabled" example:"true"`
	Rate    float64 `koanf:"rate" example:"500.0"`
	Burst   int     `koanf:"burst" example:"50"`
}

// StoreConfig configures persistent storage locations.
type StoreConfig struct {
	Dir            string `koanf:"dir" example:"./data"`
	WALMaxSizeMB   int    `koanf:"wal_max_size_mb" example:"500"`
	DatabaseFile   string `koanf:"database_file" example:"flux.duckdb"`
	ClusterKeyFile string `koanf:"cluster_key_file" example:"cluster.key"`
}

// SnakeConfig configures the NATS/JetStream connection (The Snake).
type SnakeConfig struct {
	URL                       string   `koanf:"url" example:"nats://localhost:4222"`
	Port                      int      `koanf:"port" example:"4222"`
	Domain                    string   `koanf:"domain" example:"flux"`
	StreamName                string   `koanf:"stream_name" example:"flux-msg"`
	ConnectTimeout            string   `koanf:"connect_timeout" example:"10s"`
	ReconnectWait             string   `koanf:"reconnect_wait" example:"1s"`
	OperationTimeout          string   `koanf:"operation_timeout" example:"5s"`
	SubscriptionRetryWait     string   `koanf:"subscription_retry_wait" example:"200ms"`
	SubscriptionRetryAttempts int      `koanf:"subscription_retry_attempts" example:"5"`
	InactiveThreshold         string   `koanf:"inactive_threshold" example:"30s"`
	ConvergenceDelay          string   `koanf:"convergence_delay" example:"100ms"`
	RootCAFile                string   `koanf:"root_ca_file"`
	TLSCertFile               string   `koanf:"tls_cert_file"`
	TLSKeyFile                string   `koanf:"tls_key_file"`
	InsecureSkipVerify        bool     `koanf:"insecure_skip_verify"`
	StreamSubjects            []string `koanf:"stream_subjects"`
}

// TelemetryConfig configures self-reporting metrics and logs.
type TelemetryConfig struct {
	ServiceName    string        `koanf:"service_name"`
	BatchInterval  string        `koanf:"batch_interval"`
	BaseSubject    string        `koanf:"base_subject"`
	MaxBatchSize   int           `koanf:"max_batch_size"`
	StreamName     string        `koanf:"stream_name"`
	StreamSubjects []string      `koanf:"stream_subjects"`
	Disabled       bool          `koanf:"disabled"` // Master switch to disable all telemetry
	Metrics        MetricsConfig `koanf:"metrics"`
}

// MetricsConfig toggles specific telemetry groups.
type MetricsConfig struct {
	HostEnabled    bool `koanf:"host_enabled"`
	RuntimeEnabled bool `koanf:"runtime_enabled"`
	BentoEnabled   bool `koanf:"bento_enabled"`
}
