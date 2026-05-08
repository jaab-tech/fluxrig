// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

func TestNewApp(t *testing.T) {
	cfg := &config.MixerConfig{}
	app := NewApp(cfg, "test.yaml", nil)
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.scenarioRef != "test.yaml" {
		t.Errorf("Expected scenarioRef test.yaml, got %s", app.scenarioRef)
	}
}
