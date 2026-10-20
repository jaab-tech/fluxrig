// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

const uploadToken = "upload-test-token"

func uploadServer(t *testing.T, maxPayload int) (http.Handler, manager.Manager) {
	t.Helper()
	mgr, err := manager.NewManager(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.MixerConfig{}
	cfg.API.AuthToken = uploadToken
	cfg.Mixer.MaxPayloadSize = maxPayload
	s := &Server{cfg: cfg}
	return s.WithSpecStore(mgr).Handler(), mgr
}

func upload(t *testing.T, h http.Handler, query string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/specs"+query, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+uploadToken)
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func example(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../../../examples", path))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The bridge tutorial's artefacts, uploaded in order: each translation spec
// finds the protocol specs and the lookup records it names already in the
// store, and every artefact is then in the store under its reference.
func TestUploadTheBridgeArtefacts(t *testing.T) {
	h, mgr := uploadServer(t, 2*1024*1024)
	for _, c := range []struct{ path, query, urn, kind string }{
		{"specs/iso8583-v87-ascii.yaml", "", "iso8583-v87-ascii:v3.0.0", ArtefactISO8583},
		{"specs/iso20022-rtp.yaml", "", "iso20022-rtp:v1.0.0", ArtefactISO20022},
		{"lookups/billers.yaml", "?name=billers&tag=v1", "billers:v1", ArtefactData},
		{"specs/billpay-rtp.translation.yaml", "", "billpay-rtp:v1.0.0", ArtefactTranslation},
		{"specs/rail-sim.translation.yaml", "", "rail-sim:v1.0.0", ArtefactTranslation},
	} {
		rec := upload(t, h, c.query, example(t, c.path))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body.String())
		}
		var got SpecUploaded
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.URN != c.urn || got.Kind != c.kind {
			t.Errorf("%s: filed as %s (%s), want %s (%s)", c.path, got.URN, got.Kind, c.urn, c.kind)
		}
		if _, err := mgr.Load(context.Background(), c.urn); err != nil {
			t.Errorf("%s does not resolve after the upload: %v", c.urn, err)
		}
	}
}

func TestUploadRefuses(t *testing.T) {
	h, _ := uploadServer(t, 2*1024*1024)
	billpay := example(t, "specs/billpay-rtp.translation.yaml")

	// A translation before the specs it names.
	if rec := upload(t, h, "", billpay); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "upload it first") {
		t.Errorf("translation before its specs: %d %s", rec.Code, rec.Body.String())
	}
	// A translation that names files.
	files := strings.Replace(string(billpay), `"iso8583-v87-ascii:v3.0.0"`, `"specs/iso8583-v87-ascii.yaml"`, 1)
	if rec := upload(t, h, "", []byte(files)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not files") {
		t.Errorf("translation naming files: %d %s", rec.Code, rec.Body.String())
	}
	// Data with no name and tag.
	if rec := upload(t, h, "", example(t, "lookups/billers.yaml")); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "name and tag") {
		t.Errorf("data without name: %d %s", rec.Code, rec.Body.String())
	}
	// A spec that does not load.
	if rec := upload(t, h, "", []byte("spec:\n  id: broken\n  wire:\n    source: moov-rtp20022\n")); rec.Code != http.StatusBadRequest {
		t.Errorf("broken ISO 20022 spec: %d %s", rec.Code, rec.Body.String())
	}
	// The same name:tag with other content.
	if rec := upload(t, h, "?name=billers&tag=v1", example(t, "lookups/billers.yaml")); rec.Code != http.StatusCreated {
		t.Fatalf("first upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := upload(t, h, "?name=billers&tag=v1", []byte("\"X\": {name: \"Other\"}\n")); rec.Code != http.StatusConflict {
		t.Errorf("rebinding billers:v1: %d %s", rec.Code, rec.Body.String())
	}
	// The same content again is not a conflict.
	if rec := upload(t, h, "?name=billers&tag=v1", example(t, "lookups/billers.yaml")); rec.Code != http.StatusCreated {
		t.Errorf("same content again: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadNeedsTheToken(t *testing.T) {
	h, _ := uploadServer(t, 2*1024*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/specs?name=billers&tag=v1", bytes.NewReader(example(t, "lookups/billers.yaml")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload with no token: %d", rec.Code)
	}
}

func TestUploadIsBoundedByThePayloadSize(t *testing.T) {
	h, _ := uploadServer(t, 64)
	rec := upload(t, h, "?name=billers&tag=v1", example(t, "lookups/billers.yaml"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an artefact over max_payload_size: %d %s", rec.Code, rec.Body.String())
	}
}

// The Mixer calls Listen before it reports ready, so these refusals stop its
// start instead of leaving a Mixer that runs without an API.
func TestListenRefusesWhatWouldLeaveNoAPI(t *testing.T) {
	s := &Server{cfg: &config.MixerConfig{}}
	if _, err := s.Listen("127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "api.auth_token is not set") {
		t.Fatalf("no token: %v", err)
	}
	s.cfg.API.AuthToken = uploadToken
	l, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	busy := &Server{cfg: s.cfg}
	if _, err := busy.Listen(l.Addr().String()); err == nil {
		t.Fatal("a second Listen on a bound port succeeded")
	}
}
