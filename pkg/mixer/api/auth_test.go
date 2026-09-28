// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Every finding in this file traces back to fluxrig-explained/docs/17-deep-review.md's
// "Mixer, store and enrollment" section: the management API had no auth middleware
// at all, and handlers that encoded a registry.Rack leaked its bearer Secret.

func TestHandler_UnauthenticatedRequestIsRejected(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{AuthToken: "real-token"}}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/racks"},
		{"GET", "/api/v1/config"},
		{"POST", "/api/v1/scenario/import"},
		{"POST", "/api/v1/racks/00000000-0000-0000-0000-000000000001/approve"},
		{"POST", "/api/v1/wasm/import"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Result().StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with no token: expected 401, got %d", tc.method, tc.path, w.Result().StatusCode)
		}
	}
}

func TestHandler_HealthNeedsNoAuth(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{AuthToken: "real-token"}}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected health to stay open with no token, got %d", w.Result().StatusCode)
	}
}

func TestHandler_WrongTokenIsRejected(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{AuthToken: "real-token"}}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for a wrong token, got %d", w.Result().StatusCode)
	}
}

func TestHandler_CorrectTokenIsAccepted(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{AuthToken: "real-token"}}
	mockReg := &MockRegistry{
		ListFunc: func(ctx context.Context, status string) ([]*registry.Rack, error) {
			return []*registry.Rack{{Name: "rack-1"}}, nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	req.Header.Set("Authorization", "Bearer real-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200 for the correct token, got %d", w.Result().StatusCode)
	}
}

func TestHandler_AuthDisabledDangerouslyAllowsAccess(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{AuthDisabledDangerously: true}}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected the explicit opt-out to allow access, got %d", w.Result().StatusCode)
	}
}

func TestStart_RefusesWithNoTokenAndNoExplicitOptOut(t *testing.T) {
	cfg := &config.MixerConfig{API: config.ApiConfig{Port: 0}}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	if err := s.Start("127.0.0.1:0"); err == nil {
		t.Error("expected Start to refuse with no auth_token and auth_disabled_dangerously=false")
	}
}

func TestHandleRacks_NeverLeaksSecret(t *testing.T) {
	mockReg := &MockRegistry{
		ListFunc: func(ctx context.Context, status string) ([]*registry.Rack, error) {
			return []*registry.Rack{{Name: "rack-1", Secret: "shhh-do-not-leak-me"}}, nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, uuid.Nil, uuid.Nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	w := httptest.NewRecorder()
	s.handleRacks(w, req)

	body := w.Body.String()
	if strings.Contains(body, "shhh-do-not-leak-me") {
		t.Errorf("response leaked the rack secret: %s", body)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("response still names a secret field: %s", body)
	}
}

func TestHandleConfig_NeverLeaksBootstrapSecretOrAuthToken(t *testing.T) {
	cfg := &config.MixerConfig{
		API: config.ApiConfig{Port: 8090, AuthToken: "do-not-leak-token"},
	}
	cfg.Enrollment.BootstrapSecret = "do-not-leak-bootstrap"
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, uuid.Nil, uuid.Nil, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/config", nil)
	w := httptest.NewRecorder()
	s.handleConfig(w, req)

	body := w.Body.String()
	if strings.Contains(body, "do-not-leak-token") {
		t.Errorf("response leaked the auth token: %s", body)
	}
	if strings.Contains(body, "do-not-leak-bootstrap") {
		t.Errorf("response leaked the bootstrap secret: %s", body)
	}
}
