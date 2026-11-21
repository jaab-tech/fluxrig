// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/registry/forward"
)

// A request the cluster could not answer yet is 503, so a client retries it.
// Anything else keeps the status the handler chose.
func TestErrorStatus(t *testing.T) {
	electing := &jetstream.APIError{Code: 503, ErrorCode: 10008, Description: "JetStream system temporarily unavailable"}
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"no lease holder":        {forward.ErrForbiddenNoHolder, http.StatusServiceUnavailable},
		"not the holder":         {fmt.Errorf("kvstore: approve: %w", registry.ErrNotLeaseHolder), http.StatusServiceUnavailable},
		"forwarded, unavailable": {forward.ErrorOf(forward.CodeUnavailable, "nats: no response from stream"), http.StatusServiceUnavailable},
		"local, electing":        {fmt.Errorf("kvstore: put: %w", electing), http.StatusServiceUnavailable},
		"local, no stream":       {fmt.Errorf("kvstore: put: %w", nats.ErrNoResponders), http.StatusServiceUnavailable},
		"invalid scenario":       {errors.New("scenario: gear has no type"), http.StatusBadRequest},
	} {
		if got := errorStatus(tc.err, http.StatusBadRequest); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

// failingSpecs refuses every import with one error.
type failingSpecs struct {
	manager.Manager
	err error
}

func (f failingSpecs) ImportContent(context.Context, []byte, string, string) (string, string, string, error) {
	return "", "", "", f.err
}

// A spec import that the holder could not apply during an election answers
// 503, not 400.
func TestSpecImport_UnavailableIs503(t *testing.T) {
	cfg := &config.MixerConfig{}
	cfg.Mixer.MaxPayloadSize = 256
	cfg.Registry.SpecMaxSize = 256
	err := forward.ErrorOf(forward.CodeUnavailable, "nats: no response from stream")
	s := (&Server{cfg: cfg}).WithSpecStore(failingSpecs{err: err})
	// Lookup data passes validation with no protocol spec, so the error the
	// store returns is the one that decides the status.
	body := []byte("\"k\": {\"a\": \"b\"}\n")
	if rec := postSpec(t, s, "/api/v1/specs?name=demo&tag=v1", body); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("import during an election: %d, want 503", rec.Code)
	}
}
