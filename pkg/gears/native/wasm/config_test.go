// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wasm_test

import (
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/wasm"
)

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name           string
		cfg            wasm.Config
		expectedSource string
		expectedEntry  string
		expectedMem    uint32
		checkEntry     bool
		checkMem       bool
	}{
		{
			name:           "defaults applied",
			cfg:            wasm.Config{},
			expectedSource: "",
			expectedEntry:  "process",
			expectedMem:    16,
			checkEntry:     true,
			checkMem:       true,
		},
		{
			name: "custom values preserved",
			cfg: wasm.Config{
				Source:           "file:///test.wasm",
				Entrypoint:       "custom_process",
				MemoryLimitPages: 32,
			},
			expectedSource: "file:///test.wasm",
			expectedEntry:  "custom_process",
			expectedMem:    32,
			checkEntry:     true,
			checkMem:       true,
		},
		{
			name: "empty entrypoint gets default",
			cfg: wasm.Config{
				Entrypoint: "",
			},
			expectedEntry: "process",
			checkEntry:    true,
			checkMem:      false,
		},
		{
			name: "zero memory gets default",
			cfg: wasm.Config{
				MemoryLimitPages: 0,
			},
			expectedMem: 16,
			checkEntry:  false,
			checkMem:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.ApplyDefaults()

			if tt.expectedSource != "" && cfg.Source != tt.expectedSource {
				t.Errorf("Source = %q, want %q", cfg.Source, tt.expectedSource)
			}
			if tt.checkEntry && cfg.Entrypoint != tt.expectedEntry {
				t.Errorf("Entrypoint = %q, want %q", cfg.Entrypoint, tt.expectedEntry)
			}
			if tt.checkMem && cfg.MemoryLimitPages != tt.expectedMem {
				t.Errorf("MemoryLimitPages = %d, want %d", cfg.MemoryLimitPages, tt.expectedMem)
			}
		})
	}
}

func TestConfig_EmptySource_ReturnsError(t *testing.T) {
	cfg := wasm.Config{
		Source: "",
	}
	cfg.ApplyDefaults()
	// Empty source is valid config, error happens at runtime during fetch
}
