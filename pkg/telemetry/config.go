// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "github.com/jaab-tech/fluxrig/pkg/config"

// Config holds the configuration for the Telemetry provider.
type Config struct {
	// ServiceName identifies the application (e.g. "flux-rack", "flux-mixer").
	ServiceName string
	// ServiceVersion identifies the version.
	ServiceVersion string
	// EntityID is the unique fluxEntityID (uint64) of the component.
	EntityID uint64
	// EntityName is the human-readable name of the component (e.g. "rack-nyc-01").
	EntityName string
	// Component identifies the role of the process (e.g. "RACK", "MIXER").
	// This is used to enrich logs with the top-level component attribute.
	Component string

	// NatsURL is the endpoint for the NATS cluster (Option B: NATS Transport).
	NatsURL string

	// BatchInterval is the time to buffer spans/logs before sending (for NATS integrity).
	// Default: 5s
	BatchIntervalString string

	// BaseSubject is the NATS subject prefix.
	// Default: "flux.telemetry"
	BaseSubject string

	// MaxBatchSize is the number of items to buffer before forcing a flush.
	// Default: 512
	MaxBatchSize int

	// MaxWALSizeMB moved to StoreConfig

	// Logging configuration
	Logging config.LoggingConfig

	// Stdout configuration
	StdoutEnabled bool
	StdoutLevel   string

	// Metrics Configuration
	Metrics MetricsConfig `koanf:"metrics"`

	// Store configuration (For WAL location)
	Store config.StoreConfig

	// Throttling configuration
	Throttling config.ThrottlingConfig

	// QoS: Max time to wait for NATS publish (Metrics/Spans).
	// Default: "500ms"
	QoSPublishTimeout string `koanf:"qos_publish_timeout"`
}

type MetricsConfig struct {
	HostEnabled    bool `koanf:"host_enabled"`
	RuntimeEnabled bool `koanf:"runtime_enabled"`
	BentoEnabled   bool `koanf:"bento_enabled"`
}
