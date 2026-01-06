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
	Store   StoreConfig   `koanf:"store"`
	Rack    RackSettings  `koanf:"rack"`
}

type LoggingConfig struct {
	Level      string           `koanf:"level"`
	Filename   string           `koanf:"filename"`
	MaxSizeMB  int              `koanf:"max_size_mb"`
	MaxBackups int              `koanf:"max_backups"`
	Compress   bool             `koanf:"compress"`
	Throttling ThrottlingConfig `koanf:"throttling"`
}

type StoreConfig struct {
	Dir            string `koanf:"dir"`              // Base directory (e.g. "./data")
	WALMaxSizeMB   int    `koanf:"wal_max_size_mb"`  // WAL Hard Cap
	StateFile      string `koanf:"state_file"`       // Rack State (e.g. "state.flux")
	DatabaseFile   string `koanf:"database_file"`    // Mixer DB (e.g. "fluxrig.duckdb")
	ClusterKeyFile string `koanf:"cluster_key_file"` // Cluster Key (e.g. "cluster.key")
}

type ThrottlingConfig struct {
	Enabled bool    `koanf:"enabled"`
	Rate    float64 `koanf:"rate"`
	Burst   int     `koanf:"burst"`
}

type RackSettings struct {
	Name       string `koanf:"name"`
	NamePrefix string `koanf:"name_prefix"`
	// DataDir removed (Moved to Store.Dir)
	MachineID         uint16    `koanf:"machine_id"`
	HeartbeatInterval string    `koanf:"heartbeat_interval"`
	EnrollmentTimeout string    `koanf:"enrollment_timeout"`
	Bus               BusConfig `koanf:"bus"`
}

type BusConfig struct {
	URL            string `koanf:"url"`
	StreamName     string `koanf:"stream_name"`
	ConnectTimeout string `koanf:"connect_timeout"`
	ReconnectWait  string `koanf:"reconnect_wait"`
}

// LoadRack reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadRack(path string) (*RackConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "rack.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)

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
	_ = k.Set("rack.bus.reconnect_wait", "1s")
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
