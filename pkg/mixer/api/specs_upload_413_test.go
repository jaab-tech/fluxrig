// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// A store that refuses the import with ErrTooLarge (registry.spec_max_size)
// answers 413, like an artefact over mixer.max_payload_size: the artefact
// is too large, not invalid. A generic store failure stays 400.
func TestSpecImport_TooLargeIs413(t *testing.T) {
	cfg := &config.MixerConfig{}
	cfg.Mixer.MaxPayloadSize = 1 << 20
	cfg.Registry.SpecMaxSize = 64
	// Lookup data passes validation with no protocol spec, so the error the
	// store returns is the one that decides the status.
	body := []byte("\"k\": {\"a\": \"b\"}\n")

	tooLarge := (&Server{cfg: cfg}).WithSpecStore(failingSpecs{err: manager.ErrTooLarge})
	if rec := postSpec(t, tooLarge, "/api/v1/specs?name=demo&tag=v1", body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an import the store refused as too large: %d, want 413", rec.Code)
	}

	broken := (&Server{cfg: cfg}).WithSpecStore(failingSpecs{err: errors.New("boom")})
	if rec := postSpec(t, broken, "/api/v1/specs?name=demo&tag=v1", body); rec.Code != http.StatusBadRequest {
		t.Errorf("an import the store failed: %d, want 400", rec.Code)
	}
}
