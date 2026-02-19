// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

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

// Config holds the configuration for the ISO8583 I/O Gear.
type Config struct {
	// Mode: 'server' (Listen) or 'client' (Dial)
	Mode string `json:"mode" mapstructure:"mode"`

	// Server Mode Settings
	Bind           string `json:"bind" mapstructure:"bind"`                       // e.g. ":9000"
	MaxConnections int    `json:"max_connections" mapstructure:"max_connections"` // Limit concurrent connections (0 = unlimited, default 4096)

	// Client Mode Settings
	Connect       string   `json:"connect" mapstructure:"connect"`               // e.g. "localhost:9000"
	ReconnectWait Duration `json:"reconnect_wait" mapstructure:"reconnect_wait"` // e.g. "5s"

	// Framing Settings (ISO8583 Length-Prefix)
	FrameLengthSize     int    `json:"frame_length_size" mapstructure:"frame_length_size"`         // 2 or 4 bytes for length prefix
	FrameIncludesHeader bool   `json:"frame_includes_header" mapstructure:"frame_includes_header"` // Whether length includes the header itself
	FrameLengthEndian   string `json:"frame_length_endian" mapstructure:"frame_length_endian"`     // "big" (default) or "little"

	// Advanced Framing (Variant Support)
	Variant  string `json:"variant" mapstructure:"variant"`   // "generic", "visa", "mastercard"
	Encoding string `json:"encoding" mapstructure:"encoding"` // "ascii", "ebcdic", "bcd"

	// Custom Headers (e.g. V.I.P Header)
	ProtocolHeaderSize int    `json:"protocol_header_size" mapstructure:"protocol_header_size"` // Length of the custom header (excluding framing length)
	VisaSrcID          string `json:"visa_src_id" mapstructure:"visa_src_id"`                   // V.I.P Source ID (hex)
	VisaDstID          string `json:"visa_dst_id" mapstructure:"visa_dst_id"`                   // V.I.P Destination ID (hex)

	// TPDU Settings (Optional)
	TPDUEnabled bool `json:"tpdu_enabled" mapstructure:"tpdu_enabled"` // Enable TPDU header processing
	TPDULength  int  `json:"tpdu_length" mapstructure:"tpdu_length"`   // TPDU header length (typically 5 bytes)
	TPDUSwap    bool `json:"tpdu_swap" mapstructure:"tpdu_swap"`       // Swap source/destination in response

	// Heuristic Validation (Layer 1.5)
	HeuristicValidation     bool `json:"heuristic_validation" mapstructure:"heuristic_validation"`           // Enable bitmap/MTI sanity checks
	PreserveHeaders         bool `json:"preserve_headers" mapstructure:"preserve_headers"`                   // When true, preserves raw header in metadata and reuses it on reply
	StrictConnectionRouting bool `json:"strict_connection_routing" mapstructure:"strict_connection_routing"` // If true, strictly matches conn.id. If false, falls back to any active connection (Test/Loopback context).

	// Timeouts
	ReadTimeout    Duration `json:"read_timeout" mapstructure:"read_timeout"`
	WriteTimeout   Duration `json:"write_timeout" mapstructure:"write_timeout"`
	ConnectTimeout Duration `json:"connect_timeout" mapstructure:"connect_timeout"`
	IdleTimeout    Duration `json:"idle_timeout" mapstructure:"idle_timeout"`
}

const (
	VariantGeneric    = "generic"
	VariantVisa       = "visa"
	VariantMastercard = "mastercard"

	EncodingASCII  = "ascii"
	EncodingEBCDIC = "ebcdic"
	EncodingBCD    = "bcd"

	EndianBig    = "big"
	EndianLittle = "little"
)

// DefaultConfig returns safe defaults for ISO8583 framing
func DefaultConfig() Config {
	return Config{
		Mode:                    ModeServer,
		Bind:                    ":8583",
		FrameLengthSize:         2,
		FrameIncludesHeader:     false,
		FrameLengthEndian:       EndianBig,
		TPDUEnabled:             false,
		TPDULength:              5,
		TPDUSwap:                true,
		HeuristicValidation:     true,
		StrictConnectionRouting: true,
		MaxConnections:          4096,
		ReconnectWait:           Duration(5 * time.Second),
		ReadTimeout:             Duration(30 * time.Second),
		WriteTimeout:            Duration(5 * time.Second),
		ConnectTimeout:          Duration(10 * time.Second),
		IdleTimeout:             Duration(60 * time.Second),
	}
}

// ParseConfig decodes the raw map into the Config struct.
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

// ApplyDefaults applies variant-specific defaults if not manually overridden.
func (c *Config) ApplyDefaults() {
	if c.Variant == "" {
		c.Variant = VariantGeneric
	}

	// Apply Variant Presets
	switch c.Variant {
	case VariantVisa:
		// Visa Defaults (V.I.P / Electronic Authorization)
		if c.Encoding == "" {
			c.Encoding = EncodingEBCDIC // Visa EA is strictly EBCDIC
		}
		if c.ProtocolHeaderSize == 0 {
			c.ProtocolHeaderSize = 22 // Standard V.I.P Header
		}
		if c.FrameLengthSize == 0 {
			c.FrameLengthSize = 2 // Visa typical framing
		}
		if c.FrameLengthEndian == "" {
			c.FrameLengthEndian = EndianBig
		}

	case VariantMastercard:
		// Mastercard (MIP or SMS)
		// Both use standard framing (Length=2, Header=0)
		if c.ProtocolHeaderSize == 0 {
			c.ProtocolHeaderSize = 0
		}
		if c.Encoding == "" {
			c.Encoding = EncodingASCII // Mastercard SMS is usually ASCII
		}
	}

	// Final Fallback
	if c.Encoding == "" {
		c.Encoding = EncodingASCII
	}
	if c.FrameLengthEndian == "" {
		c.FrameLengthEndian = EndianBig
	}
}

// Validate ensures the config is consistent.
func (c *Config) Validate() error {
	c.ApplyDefaults()

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

	if c.FrameLengthSize != 2 && c.FrameLengthSize != 4 {
		return fmt.Errorf("frame_length_size must be 2 or 4, got %d", c.FrameLengthSize)
	}

	if c.FrameLengthEndian != EndianBig && c.FrameLengthEndian != EndianLittle {
		return fmt.Errorf("invalid frame_length_endian: %s (must be 'big' or 'little')", c.FrameLengthEndian)
	}

	return nil
}

// SchemaJSON returns the JSON Schema for validation.
func SchemaJSON() string {
	return `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "mode": { "type": "string", "enum": ["server", "client"] },
    "bind": { "type": "string" },
    "connect": { "type": "string" },
    "reconnect_wait": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" },
    "frame_length_size": { "type": "integer", "enum": [2, 4] },
    "frame_includes_header": { "type": "boolean" },
    "frame_length_endian": { "type": "string", "enum": ["big", "little"] },
    "variant": { "type": "string", "enum": ["generic", "visa", "mastercard"] },
    "encoding": { "type": "string", "enum": ["ascii", "ebcdic", "bcd"] },
    "protocol_header_size": { "type": "integer" },
    "tpdu_enabled": { "type": "boolean" },
    "tpdu_length": { "type": "integer" },
    "tpdu_swap": { "type": "boolean" },
    "heuristic_validation": { "type": "boolean" },
    "read_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" },
    "write_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" },
    "connect_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" },
    "idle_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$" }
  },
  "required": ["mode"]
}`
}
