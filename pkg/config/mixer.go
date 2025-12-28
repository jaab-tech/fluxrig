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
	Mixer         MixerSettings       `koanf:"mixer"`
	API           ApiConfig           `koanf:"api"`
	Snake         SnakeConfig         `koanf:"snake"`
	Store         StoreConfig         `koanf:"store"`
	Observability ObservabilityConfig `koanf:"observability"`
}

type ObservabilityConfig struct {
	Enabled  bool           `koanf:"enabled"`
	Tier     string         `koanf:"tier"`
	Embedded EmbeddedConfig `koanf:"embedded"`
}

type EmbeddedConfig struct {
	DataDir       string `koanf:"data_dir"`
	FlushInterval string `koanf:"flush_interval"`
}

type MixerSettings struct {
	MachineID uint16 `koanf:"machine_id"` // e.g. 1 (Reserved 1-99 for Mixers)
}

type ApiConfig struct {
	Port int `koanf:"port"` // e.g. 8090
}

type SnakeConfig struct {
	Port          int      `koanf:"port"`            // e.g. 4222
	URL           string   `koanf:"url"`             // e.g. "nats://localhost:4222"
	ClusterName   string   `koanf:"cluster_name"`    // e.g. "flux"
	StoreDir      string   `koanf:"store_dir"`       // e.g. "./data/js"
	StreamName    string   `koanf:"stream_name"`     // e.g. "flux"
	StartSubjects []string `koanf:"stream_subjects"` // e.g. ["fluxrig.>"]
}

type StoreConfig struct {
	Path           string `koanf:"path"`             // e.g. "data/fluxrig.duckdb"
	ClusterKeyPath string `koanf:"cluster_key_path"` // e.g. "data/cluster.key"
}

// LoadMixer reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults
func LoadMixer(path string) (*MixerConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("logging.level", "info")
	_ = k.Set("mixer.machine_id", 1) // Default to 1 (First Mixer)
	_ = k.Set("api.port", 8090)
	_ = k.Set("snake.port", 4222)
	_ = k.Set("snake.url", "nats://localhost:4222")
	_ = k.Set("snake.cluster_name", "flux")
	_ = k.Set("snake.store_dir", "data/js")
	_ = k.Set("snake.stream_name", "flux")
	_ = k.Set("snake.stream_subjects", []string{"fluxrig.>", "flux.telemetry.>"})
	_ = k.Set("store.path", "data/fluxrig.duckdb")
	_ = k.Set("store.cluster_key_path", "data/cluster.key")
	_ = k.Set("observability.tier", "embedded")
	_ = k.Set("observability.embedded.data_dir", "./data/telemetry")
	_ = k.Set("observability.embedded.flush_interval", "5s")

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
