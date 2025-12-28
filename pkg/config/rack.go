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
	Logging LoggingConfig `koanf:"logging"`
	Rack    RackSettings  `koanf:"rack"`
}

type LoggingConfig struct {
	Level string `koanf:"level"`
	Dir   string `koanf:"dir"`
}

type RackSettings struct {
	Name              string    `koanf:"name"`
	NamePrefix        string    `koanf:"name_prefix"`
	DataDir           string    `koanf:"data_dir"`
	StateFile         string    `koanf:"state_file"`
	MachineID         uint16    `koanf:"machine_id"`
	HeartbeatInterval string    `koanf:"heartbeat_interval"` // e.g. "30s"
	EnrollmentTimeout string    `koanf:"enrollment_timeout"` // e.g. "2s"
	Bus               BusConfig `koanf:"bus"`
}

type BusConfig struct {
	URL            string `koanf:"url"`
	StreamName     string `koanf:"stream_name"`     // e.g. "flux"
	ConnectTimeout string `koanf:"connect_timeout"` // e.g. "10s"
	ReconnectWait  string `koanf:"reconnect_wait"`  // e.g. "1s"
}

// LoadRack reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadRack(path string) (*RackConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.dir", "") // Default to stdout only (12-factor)
	_ = k.Set("logging.max_size", 100)
	_ = k.Set("logging.max_backups", 3)
	_ = k.Set("logging.max_age", 28)
	_ = k.Set("logging.compress", true)

	_ = k.Set("rack.data_dir", "./data")
	_ = k.Set("rack.bus.url", "nats://localhost:4222")
	// _ = k.Set("rack.name", "rack-default") // Disabled to allow Zero-Config
	_ = k.Set("rack.name_prefix", "node-")
	_ = k.Set("rack.state_file", "state.flux")
	_ = k.Set("rack.bus.stream_name", "flux") // Default to "flux"
	_ = k.Set("rack.machine_id", 0)
	_ = k.Set("rack.heartbeat_interval", "30s")
	_ = k.Set("rack.enrollment_timeout", "2s")
	_ = k.Set("rack.bus.connect_timeout", "10s")
	_ = k.Set("rack.bus.reconnect_wait", "1s")

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
