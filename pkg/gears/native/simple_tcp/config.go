package simple_tcp

import (
	"encoding/json"
	"fmt"
	"time"
)

// Mode enum
const (
	ModeServer = "server"
	ModeClient = "client"
)

// Duration is a wrapper around time.Duration to support JSON string unmarshalling
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = Duration(value)
		return nil
	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(tmp)
		return nil
	default:
		return fmt.Errorf("invalid duration")
	}
}

// Config holds the configuration for the Simple TCP Gear.
type Config struct {
	// Mode: 'server' (Listen) or 'client' (Dial)
	Mode string `json:"mode"`

	// Server Mode Settings
	Bind string `json:"bind"` // e.g. ":9000"

	// Client Mode Settings
	Connect       string   `json:"connect"`        // e.g. "localhost:9000"
	ReconnectWait Duration `json:"reconnect_wait"` // e.g. "5s"

	// Common Settings
	Delimiter         string   `json:"delimiter"`          // Delimiter string (e.g. "\n", "<log")
	DelimiterPosition string   `json:"delimiter_position"` // "suffix" (default) or "prefix"
	DelimiterInclude  bool     `json:"delimiter_include"`  // Ingress: Keep delimiter in payload
	DelimiterAppend   bool     `json:"delimiter_append"`   // Egress: Append delimiter to outgoing
	IdleTimeout       Duration `json:"idle_timeout"`       // Close connection if idle
}

// DefaultConfig returns safe defaults
func DefaultConfig() Config {
	return Config{
		Mode:              ModeServer,
		Bind:              ":8080",
		ReconnectWait:     Duration(5 * time.Second),
		DelimiterPosition: "suffix",
		IdleTimeout:       Duration(60 * time.Second),
	}
}

// ParseConfig decodes the raw map into the Config struct.
// We use JSON round-trip to respect the json tags.
func ParseConfig(raw map[string]any) (*Config, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal raw config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

// Validate ensures the config is consistent.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeServer:
		if c.Bind == "" {
			return fmt.Errorf("mode 'server' requires 'bind' address")
		}
	case ModeClient:
		if c.Connect == "" {
			return fmt.Errorf("mode 'client' requires 'connect' address")
		}
	default:
		return fmt.Errorf("invalid mode: %s", c.Mode)
	}
	return nil
}

// SchemaJSON returns the JSON Schema for validation (ADR 0005 Requirement).
// Hardcoded for Phase 3.
func SchemaJSON() string {
	return `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "mode": { "type": "string", "enum": ["server", "client"] },
    "bind": { "type": "string" },
    "connect": { "type": "string" },
    "reconnect_wait": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" },
    "delimiter": { "type": "string" },
    "idle_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" }
  },
  "required": ["mode"]
}`
}
