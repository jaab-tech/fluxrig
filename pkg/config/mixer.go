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
}

type EnrollmentConfig struct {
	PushDelay string `koanf:"push_delay"` // e.g. "1s"
}

type ObservabilityConfig struct {
	Enabled  bool           `koanf:"enabled"`
	Tier     string         `koanf:"tier"`
	Embedded EmbeddedConfig `koanf:"embedded"`
}

type EmbeddedConfig struct {
	FlushInterval string `koanf:"flush_interval"`
	// DataDir removed (Uses Store.Dir/telemetry)
}

type MixerSettings struct {
	MachineID uint16 `koanf:"machine_id"`
	MixerName string `koanf:"mixer_name"`
}

type ApiConfig struct {
	Port int `koanf:"port"` // e.g. 8090
}

type SnakeConfig struct {
	Port        int    `koanf:"port"`
	URL         string `koanf:"url"`
	ClusterName string `koanf:"cluster_name"`
	// StoreDir removed (Uses Store.Dir/nats)
	StreamName    string   `koanf:"stream_name"`
	StartSubjects []string `koanf:"stream_subjects"`
	Durable       bool     `koanf:"durable"`
}

// StoreConfig is defined in rack.go (shared package config)

// LoadMixer reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadMixer(path string) (*MixerConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "mixer.log")
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
	_ = k.Set("snake.port", 4222)
	_ = k.Set("snake.url", "nats://localhost:4222")
	_ = k.Set("snake.cluster_name", "flux")
	// snake.store_dir removed
	_ = k.Set("snake.stream_name", "")
	_ = k.Set("snake.stream_subjects", []string{})
	_ = k.Set("snake.durable", false)

	_ = k.Set("observability.tier", "embedded")
	// observability.embedded.data_dir removed
	_ = k.Set("observability.embedded.flush_interval", "5s")
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
