package bento

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config represents the raw configuration structure passed from FluxRig.
// It matches the 'config' block in the rack.toml/scenario.yaml.
type Config struct {
	// LogLevel optionally overrides the global log level for this gear.
	// Values: "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "OFF"
	LogLevel string `mapstructure:"log_level"`

	// Bento holds the native Bento configuration as a raw map.
	// We will marshal this back to YAML to let Bento's engine parse it.
	Bento map[string]any `mapstructure:"bento"`
}

// MapToYaml converts a generic map[string]any into a YAML byte slice.
// This is required because Bento's config parser expects YAML bytes.
func MapToYaml(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("bento config map is nil")
	}
	return yaml.Marshal(m)
}
