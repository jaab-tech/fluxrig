// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

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

// Framing mode for messages
const (
	FramingDelimiter     = "delimiter"      // Delimiter-based framing (default)
	FramingLengthPrefix2 = "length_prefix2" // 2-byte big-endian length prefix
	FramingLengthPrefix4 = "length_prefix4" // 4-byte big-endian length prefix
)

// Config holds the configuration for the Simple TCP Gear.
type Config struct {
	// Mode: 'server' (Listen) or 'client' (Dial)
	Mode string `json:"mode" mapstructure:"mode"`

	// Server Mode Settings
	Bind           string `json:"bind" mapstructure:"bind"`                       // e.g. ":9000"
	MaxConnections int    `json:"max_connections" mapstructure:"max_connections"` // Limit concurrent connections (0 = unlimited, default 4096)

	// Client Mode Settings
	Connect       string   `json:"connect" mapstructure:"connect"`               // e.g. "localhost:9000"
	ReconnectWait Duration `json:"reconnect_wait" mapstructure:"reconnect_wait"` // e.g. "5s"
	ReadResponses bool     `json:"read_responses" mapstructure:"read_responses"` // Client: read responses from server (default true)

	// Framing Settings
	Framing           string `json:"framing" mapstructure:"framing"`                       // "delimiter" (default), "length_prefix2", "length_prefix4"
	Delimiter         string `json:"delimiter" mapstructure:"delimiter"`                   // Delimiter string (e.g. "\n", "<log")
	DelimiterPosition string `json:"delimiter_position" mapstructure:"delimiter_position"` // "suffix" (default) or "prefix"
	DelimiterInclude  bool   `json:"delimiter_include" mapstructure:"delimiter_include"`   // Ingress: Keep delimiter in payload
	DelimiterAppend   bool   `json:"delimiter_append" mapstructure:"delimiter_append"`     // Egress: Append delimiter to outgoing

	// Common Settings
	IdleTimeout Duration `json:"idle_timeout" mapstructure:"idle_timeout"` // Close connection if idle

	// Server Mode: outbound traffic with no connection to reach yet (nothing
	// connected, or the message named no conn.id at all) queues here instead
	// of being dropped. 0 selects the default (1000); a message whose named
	// conn.id belongs to a connection that is gone is never queued here, it
	// is refused (see server.go, Process).
	MaxBufferedMessages int `json:"max_buffered_messages" mapstructure:"max_buffered_messages"`
}

// DefaultConfig returns safe defaults
func DefaultConfig() Config {
	return Config{
		Mode:                ModeServer,
		Bind:                ":8080",
		MaxConnections:      4096,
		ReconnectWait:       Duration(5 * time.Second),
		ReadResponses:       true,
		Framing:             FramingDelimiter,
		DelimiterPosition:   "suffix",
		IdleTimeout:         Duration(60 * time.Second),
		MaxBufferedMessages: defaultMaxBufferedMessages,
	}
}

// defaultMaxBufferedMessages is used when MaxBufferedMessages is left unset
// (0), including by a Config built directly rather than through ParseConfig.
const defaultMaxBufferedMessages = 1000

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

// SchemaJSON returns the JSON Schema for validation.
// Hardcoded for Phase 3.
func SchemaJSON() string {
	return `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "mode": { "type": "string", "enum": ["server", "client"], "description": "server (listen for connections) or client (dial an upstream)." },
    "bind": { "type": "string", "default": ":8080", "description": "server mode: address to listen on, e.g. ':9000'." },
    "connect": { "type": "string", "description": "client mode: upstream address to dial, host:port." },
    "reconnect_wait": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5s", "description": "client mode: delay before redialing a dropped connection, e.g. '1s'." },
    "read_responses": { "type": "boolean", "default": true, "description": "client mode: whether to read responses from the server. Set to false for one-way send (e.g. to an echo server that doesn't send proper ISO8583 responses)." },
    "max_connections": { "type": "integer", "default": 4096, "description": "server mode: maximum concurrent connections (0 = unlimited)." },
    "framing": { "type": "string", "enum": ["delimiter", "length_prefix2", "length_prefix4"], "default": "delimiter", "description": "message framing mode: delimiter (newline, etc), length_prefix2 (2-byte BE), length_prefix4 (4-byte BE)." },
    "delimiter": { "type": "string", "description": "message delimiter for framing (e.g. a newline, 0x03); if empty, frames on newline (ScanLines)." },
    "delimiter_position": { "type": "string", "enum": ["suffix", "prefix"], "default": "suffix", "description": "where the delimiter sits relative to the message: suffix or prefix." },
    "delimiter_include": { "type": "boolean", "default": false, "description": "ingress: keep the delimiter in the emitted payload." },
    "delimiter_append": { "type": "boolean", "default": false, "description": "egress: append the delimiter to outgoing payloads." },
    "idle_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "60s", "description": "max time a connection may sit idle before it is closed." },
    "max_buffered_messages": { "type": "integer", "default": 1000, "description": "server mode: outbound messages queued when nothing is connected yet, or when a message names no conn.id (0 selects the default)." }
  },
  "required": ["mode"]
}`
}
