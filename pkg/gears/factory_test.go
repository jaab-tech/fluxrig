// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

type MockGear struct{}

func (m *MockGear) Init(ctx sdk.GearContext) error                               { return nil }
func (m *MockGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error { return nil }
func (m *MockGear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	return msg, nil
}
func (m *MockGear) Stop() error {
	return nil
}

func (m *MockGear) Drain(ctx context.Context) error {
	return nil
}

func TestFactory(t *testing.T) {
	f := NewFactory()

	// 1. Built-in
	g, err := f.Create("io_tcp")
	if err != nil {
		t.Errorf("Create(io_tcp) failed: %v", err)
	}
	if g == nil {
		t.Error("Returned nil gear")
	}

	// 2. Custom Registration
	f.Register("mock", func() sdk.NativeGear { return &MockGear{} })
	m, err := f.Create("mock")
	if err != nil {
		t.Errorf("Create(mock) failed: %v", err)
	}
	if _, ok := m.(*MockGear); !ok {
		t.Error("Did not return MockGear")
	}

	// 3. Unknown
	_, err = f.Create("unknown_gear_type")
	if err == nil {
		t.Error("Expected error for unknown type")
	}
}

// TestManifestCoverage enforces the ADR 0045 contract: every registered gear
// has a manifest whose type matches, a category, ports, and a config schema
// (except gears documented as taking none). Fails loudly when a new gear is
// registered without a manifest, so the catalog cannot silently regress.
func TestManifestCoverage(t *testing.T) {
	f := NewFactory()
	for _, typ := range f.Types() {
		m, ok := f.Manifest(typ)
		if !ok {
			t.Errorf("%s: no manifest recorded", typ)
			continue
		}
		if m.Type != typ {
			t.Errorf("%s: manifest.Type = %q, want %q", typ, m.Type, typ)
		}
		if m.Category == "" {
			t.Errorf("%s: manifest has no category", typ)
		}
		if m.Summary == "No manifest declared yet." {
			t.Errorf("%s: gear does not declare a manifest (implement sdk.Manifested)", typ)
		}
		if len(m.Ports) == 0 {
			t.Errorf("%s: manifest declares no ports", typ)
		}
		// Config schema, when present, must be valid JSON.
		if m.ConfigSchema != "" {
			var js any
			if err := json.Unmarshal([]byte(m.ConfigSchema), &js); err != nil {
				t.Errorf("%s: config schema is not valid JSON: %v", typ, err)
			}
		}
	}
}
