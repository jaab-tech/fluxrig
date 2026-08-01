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
	// Mode: 'server' (Listen) or 'client' (Dial).
	Mode string `json:"mode" mapstructure:"mode"`

	// Server Mode Settings
	// Bind: The local address and port to listen on (e.g. ":9000").
	Bind string `json:"bind" mapstructure:"bind"`
	// MaxConnections: Limit concurrent connections. Default: 4096.
	MaxConnections int `json:"max_connections" mapstructure:"max_connections"`

	// TLS secures the external socket (dial in client mode, listener in
	// server mode). Off by default.
	TLS TLSConfig `json:"tls" mapstructure:"tls"`

	// Client Mode Settings
	// Connect: Remote address to dial (e.g. "localhost:9000").
	Connect string `json:"connect" mapstructure:"connect"`
	// ReconnectWait: Delay between reconnection attempts. Default: 5s.
	ReconnectWait Duration `json:"reconnect_wait" mapstructure:"reconnect_wait"`

	// Framing Settings (ISO8583 Length-Prefix)
	// FrameLengthSize: Number of bytes for length prefix (2 or 4). Default: 2.
	FrameLengthSize int `json:"frame_length_size" mapstructure:"frame_length_size"`
	// FrameIncludesHeader: Whether the length prefix includes its own size. Default: false.
	FrameIncludesHeader bool `json:"frame_includes_header" mapstructure:"frame_includes_header"`
	// FrameLengthEndian: Endianness of the length prefix ("big" or "little"). Default: "big".
	FrameLengthEndian string `json:"frame_length_endian" mapstructure:"frame_length_endian"`

	// Advanced Framing (Variant Support)
	// Variant: Protocol variant ("generic", "visa", "mastercard"). Default: "generic".
	Variant string `json:"variant" mapstructure:"variant"`
	// Encoding: Message encoding ("ascii", "ebcdic", "bcd"). Default: "ascii".
	Encoding string `json:"encoding" mapstructure:"encoding"`

	// Custom Headers (e.g. V.I.P Header)
	// ProtocolHeaderSize: Length of the custom header (excluding framing length). Default: 0.
	ProtocolHeaderSize int `json:"protocol_header_size" mapstructure:"protocol_header_size"`
	// VisaSrcID: V.I.P Source ID (hex).
	VisaSrcID string `json:"visa_src_id" mapstructure:"visa_src_id"`
	// VisaDstID: V.I.P Destination ID (hex).
	VisaDstID string `json:"visa_dst_id" mapstructure:"visa_dst_id"`

	// TPDU Settings (Optional)
	// TPDUEnabled: Enable TPDU header processing. Default: false.
	TPDUEnabled bool `json:"tpdu_enabled" mapstructure:"tpdu_enabled"`
	// TPDULength: TPDU header length (typically 5 bytes). Default: 5.
	TPDULength int `json:"tpdu_length" mapstructure:"tpdu_length"`
	// TPDUSwap: Swap source/destination in response. Default: true.
	TPDUSwap bool `json:"tpdu_swap" mapstructure:"tpdu_swap"`

	// Heuristic Validation (Layer 1.5)
	// HeuristicValidation: Enable bitmap/MTI sanity checks. Default: true.
	HeuristicValidation bool `json:"heuristic_validation" mapstructure:"heuristic_validation"`
	// PreserveHeaders: Preserves raw headers in metadata for echo/loopback support. Default: false.
	PreserveHeaders bool `json:"preserve_headers" mapstructure:"preserve_headers"`
	// StrictConnectionRouting: strictly matches conn.id. Default: true.
	StrictConnectionRouting bool `json:"strict_connection_routing" mapstructure:"strict_connection_routing"`

	// UnsafeRawFrameLog dumps raw frame/payload bytes (hex) at TRACE. This
	// exposes cardholder data (PAN) and Sensitive Authentication Data (track
	// data, PIN block, CVV) and MUST NOT be enabled inside a Cardholder Data
	// Environment (PCI DSS Req. 3.2/3.4). Default: false. For protocol
	// debugging in non-CDE test environments only.
	UnsafeRawFrameLog bool `json:"unsafe_raw_frame_log" mapstructure:"unsafe_raw_frame_log"`

	// Timeouts
	// ReadTimeout: Max time to wait for a frame. Default: 5s.
	ReadTimeout Duration `json:"read_timeout" mapstructure:"read_timeout"`
	// WriteTimeout: Max time for a socket write. Default: 2s.
	WriteTimeout Duration `json:"write_timeout" mapstructure:"write_timeout"`
	// ConnectTimeout: Max time for TCP dial. Default: 5s.
	ConnectTimeout Duration `json:"connect_timeout" mapstructure:"connect_timeout"`
	// IdleTimeout: Max time for idle connection before closing. Default: 15s.
	IdleTimeout Duration `json:"idle_timeout" mapstructure:"idle_timeout"`
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

	// Default values for ISO8583 Framing and Lifecycle
	DefaultMaxConnections  = 4096
	DefaultFrameLengthSize = 2
	DefaultTPDULength      = 5
	DefaultProtocolHeader  = 0
	DefaultVisaHeaderSize  = 22

	DefaultReconnectWait  = 5 * time.Second
	DefaultReadTimeout    = 5 * time.Second
	DefaultWriteTimeout   = 2 * time.Second
	DefaultConnectTimeout = 5 * time.Second
	DefaultIdleTimeout    = 15 * time.Second

	DefaultEncoding = EncodingASCII
	DefaultVariant  = VariantGeneric
	DefaultEndian   = EndianBig
)

// DefaultConfig returns safe defaults for ISO8583 framing
func DefaultConfig() Config {
	return Config{
		Mode:                    ModeServer,
		Bind:                    "",
		FrameLengthSize:         DefaultFrameLengthSize,
		FrameIncludesHeader:     false,
		FrameLengthEndian:       DefaultEndian,
		TPDUEnabled:             false,
		TPDULength:              DefaultTPDULength,
		TPDUSwap:                true,
		HeuristicValidation:     true,
		StrictConnectionRouting: true,
		MaxConnections:          DefaultMaxConnections,
		ReconnectWait:           Duration(DefaultReconnectWait),
		ReadTimeout:             Duration(DefaultReadTimeout),
		WriteTimeout:            Duration(DefaultWriteTimeout),
		ConnectTimeout:          Duration(DefaultConnectTimeout),
		IdleTimeout:             Duration(DefaultIdleTimeout),
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
		c.Variant = DefaultVariant
	}

	// Apply Variant Presets
	switch c.Variant {
	case VariantVisa:
		// Visa Defaults (V.I.P / Electronic Authorization)
		if c.Encoding == "" {
			c.Encoding = EncodingEBCDIC // Visa EA is strictly EBCDIC
		}
		if c.ProtocolHeaderSize == 0 {
			c.ProtocolHeaderSize = DefaultVisaHeaderSize // Standard V.I.P Header
		}
		if c.FrameLengthSize == 0 {
			c.FrameLengthSize = DefaultFrameLengthSize
		}
		if c.FrameLengthEndian == "" {
			c.FrameLengthEndian = DefaultEndian
		}

	case VariantMastercard:
		// Mastercard (MIP or SMS)
		// Both use standard framing (Length=2, Header=0)
		if c.ProtocolHeaderSize == 0 {
			c.ProtocolHeaderSize = DefaultProtocolHeader
		}
		if c.Encoding == "" {
			c.Encoding = DefaultEncoding
		}
	}

	// Final Fallback
	if c.Encoding == "" {
		c.Encoding = DefaultEncoding
	}
	if c.FrameLengthEndian == "" {
		c.FrameLengthEndian = DefaultEndian
	}
	// The documented default (2) applies to every variant, not only Visa.
	if c.FrameLengthSize == 0 {
		c.FrameLengthSize = DefaultFrameLengthSize
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
    "mode": { "type": "string", "enum": ["server", "client"], "description": "server (listen for terminal/peer connections) or client (dial an upstream)." },
    "bind": { "type": "string", "description": "server mode: address to listen on, e.g. ':8583'." },
    "connect": { "type": "string", "description": "client mode: upstream address to dial, host:port." },
    "reconnect_wait": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5s", "description": "client mode: delay before redialing a dropped connection, e.g. '5s'." },
    "frame_length_size": { "type": "integer", "enum": [2, 4], "default": 2, "description": "bytes in the length prefix that frames each message (2 or 4)." },
    "frame_includes_header": { "type": "boolean", "default": false, "description": "whether the length prefix counts its own bytes." },
    "frame_length_endian": { "type": "string", "enum": ["big", "little"], "default": "big", "description": "byte order of the length prefix." },
    "variant": { "type": "string", "enum": ["generic", "visa", "mastercard"], "default": "generic", "description": "framing profile: generic, visa (VAP header) or mastercard." },
    "encoding": { "type": "string", "enum": ["ascii", "ebcdic", "bcd"], "default": "ascii", "description": "field encoding used for heuristic inspection: ascii, ebcdic or bcd." },
    "protocol_header_size": { "type": "integer", "description": "fixed protocol-header length (bytes) carried before the ISO message; 0 = none." },
    "tpdu_enabled": { "type": "boolean", "default": false, "description": "whether a 5-byte TPDU precedes the message." },
    "tpdu_length": { "type": "integer", "default": 5, "description": "TPDU length in bytes when enabled." },
    "tpdu_swap": { "type": "boolean", "default": true, "description": "swap the TPDU source/destination addresses on the reply." },
    "heuristic_validation": { "type": "boolean", "default": true, "description": "run lightweight MTI/bitmap sanity checks on each frame read." },
    "preserve_headers": { "type": "boolean", "description": "carry the raw protocol header through so the reply can mirror it." },
    "strict_connection_routing": { "type": "boolean", "default": true, "description": "return each reply on the exact connection (conn.id) its request arrived on." },
    "max_connections": { "type": "integer", "default": 4096, "description": "server mode: maximum concurrent connections (0 = unlimited)." },
    "visa_src_id": { "type": "string", "description": "visa variant: source station id in the VAP header." },
    "visa_dst_id": { "type": "string", "description": "visa variant: destination station id in the VAP header." },
    "unsafe_raw_frame_log": { "type": "boolean", "default": false, "description": "log raw frame bytes (hex) at TRACE. Off by default; raw frames carry PAN/SAD, so never enable inside a CDE." },
    "tls": {
      "type": "object",
      "description": "native TLS/mTLS for this socket.",
      "properties": {
        "enabled": { "type": "boolean", "description": "enable TLS on the socket." },
        "cert_file": { "type": "string", "description": "PEM certificate presented by this gear." },
        "key_file": { "type": "string", "description": "PEM private key for cert_file." },
        "ca_file": { "type": "string", "description": "PEM CA bundle used to verify the peer." },
        "server_name": { "type": "string", "description": "expected server name (SNI / verification)." },
        "client_auth": { "type": "boolean", "description": "require and verify a client certificate (mTLS)." }
      }
    },
    "read_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5s", "description": "max time to read the body of a frame already in progress, e.g. '5s'." },
    "write_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "2s", "description": "max time for a single socket write." },
    "connect_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "5s", "description": "client mode: max time for the TCP dial." },
    "idle_timeout": { "type": "string", "pattern": "^[0-9]+(s|ms|m|h)$", "default": "15s", "description": "max time a connection may sit idle between frames before it is closed." }
  },
  "required": ["mode"]
}`
}
