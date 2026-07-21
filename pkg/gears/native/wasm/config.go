// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wasm

// Config defines the configuration for the Wasm gear.
type Config struct {
	// Source is the bucket/key to fetch the Wasm module from NATS Object Store.
	// Format: "snake://bucket_name/object_name.wasm" or "file://local/path.wasm"
	Source string `koanf:"source" json:"source" mapstructure:"source"`

	// Entrypoint is the exported function name to process messages.
	Entrypoint string `koanf:"entrypoint" json:"entrypoint" mapstructure:"entrypoint"`

	// MemoryLimitPages defines the maximum memory pages (64KB each) for the sandbox.
	MemoryLimitPages uint32 `koanf:"memory_limit_pages" json:"memory_limit_pages" mapstructure:"memory_limit_pages"`
}

// ApplyDefaults ensures minimum defaults are set.
func (c *Config) ApplyDefaults() {
	if c.Entrypoint == "" {
		c.Entrypoint = "process"
	}
	if c.MemoryLimitPages == 0 {
		c.MemoryLimitPages = 16 // 1MB default
	}
}
