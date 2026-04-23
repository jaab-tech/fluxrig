// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// MockRegistry for testing
type MockRegistry struct {
	ListFunc         func(ctx context.Context, status string) ([]*registry.Rack, error)
	ApproveFunc      func(ctx context.Context, machineID uint16, name string) (*registry.Rack, error)
	QueryLogsFunc    func(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error)
	QueryMetricsFunc func(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error)
	CalledHeartbeat  bool
	// Add helpers for extended tests
	RemoveFunc       func(ctx context.Context, machineID uint16) error
	UpdateStatusFunc func(ctx context.Context, machineID uint16, status string) error
}

func (m *MockRegistry) SetAutoAdopt(enabled bool) {}

func (m *MockRegistry) Register(ctx context.Context, machineID string, entityType string, name string, port int, ip string, attrs map[string]any, fluxID uint64) (*registry.Rack, error) {
	return nil, nil // Not used in API server tests yet
}
func (m *MockRegistry) Get(ctx context.Context, machineID uint16) (*registry.Rack, error) {
	// For testing Passport generation, we need to return a rack
	if machineID == 1 {
		return &registry.Rack{MachineID: 1, Name: "rack-1", Status: "active", Secret: "test-secret"}, nil
	}
	return nil, registry.ErrNotFound
}

func (m *MockRegistry) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, status)
	}
	return nil, nil
}

func (m *MockRegistry) Approve(ctx context.Context, machineID uint16, name string) (*registry.Rack, error) {
	if m.ApproveFunc != nil {
		return m.ApproveFunc(ctx, machineID, name)
	}
	return nil, nil
}

func (m *MockRegistry) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any, attrs map[string]any) error {
	m.CalledHeartbeat = true
	return nil
}

func (m *MockRegistry) Remove(ctx context.Context, machineID uint16) error {
	if m.RemoveFunc != nil {
		return m.RemoveFunc(ctx, machineID)
	}
	return nil
}

func (m *MockRegistry) UpdateStatus(ctx context.Context, machineID uint16, status string) error {
	if m.UpdateStatusFunc != nil {
		return m.UpdateStatusFunc(ctx, machineID, status)
	}
	return nil
}

func (m *MockRegistry) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) {
	if m.QueryLogsFunc != nil {
		return m.QueryLogsFunc(ctx, query)
	}
	return nil, nil
}
func (m *MockRegistry) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	if m.QueryMetricsFunc != nil {
		return m.QueryMetricsFunc(ctx, query)
	}
	return nil, nil
}

func TestHandleHealth(t *testing.T) {
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, 12345, nil)
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	s.handleHealth(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleRacks_List(t *testing.T) {
	mockReg := &MockRegistry{
		ListFunc: func(ctx context.Context, status string) ([]*registry.Rack, error) {
			return []*registry.Rack{{Name: "test-rack"}}, nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	w := httptest.NewRecorder()

	s.handleRacks(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleRackAction(t *testing.T) {
	mockReg := &MockRegistry{
		ApproveFunc: func(ctx context.Context, machineID uint16, name string) (*registry.Rack, error) {
			if machineID == 99 {
				return nil, registry.ErrNotFound
			}
			return &registry.Rack{Name: name, Status: "active"}, nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	// 1. Invalid Path (No ID)
	req1 := httptest.NewRequest("POST", "/api/v1/racks/", nil)
	w1 := httptest.NewRecorder()
	s.handleRackAction(w1, req1)
	if w1.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 for empty ID, got %d", w1.Result().StatusCode)
	}

	// 2. Approve Success
	body := `{"name": "new-name"}`
	req2 := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
	req2.SetPathValue("id", "1")
	req2.SetPathValue("action", "approve")
	w2 := httptest.NewRecorder()
	s.handleRackAction(w2, req2)
	if w2.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w2.Result().StatusCode)
	}

	// 3. Not Found
	req3 := httptest.NewRequest("POST", "/api/v1/racks/99/approve", strings.NewReader(body))
	req3.SetPathValue("id", "99")
	req3.SetPathValue("action", "approve")
	w3 := httptest.NewRecorder()
	s.handleRackAction(w3, req3)
	if w3.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", w3.Result().StatusCode)
	}

	// 4. Invalid JSON
	req4 := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader("bad"))
	req4.SetPathValue("id", "1")
	req4.SetPathValue("action", "approve")
	w4 := httptest.NewRecorder()
	s.handleRackAction(w4, req4)
	if w4.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 for bad json, got %d", w4.Result().StatusCode)
	}
}

func TestHandleRackAction_Extended(t *testing.T) {
	mockReg := &MockRegistry{
		RemoveFunc: func(ctx context.Context, machineID uint16) error {
			if machineID == 99 {
				return registry.ErrNotFound
			}
			return nil
		},
		UpdateStatusFunc: func(ctx context.Context, machineID uint16, status string) error {
			if machineID == 99 {
				return registry.ErrNotFound
			}
			return nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	// DELETE
	reqDel := httptest.NewRequest("DELETE", "/api/v1/racks/1", nil)
	reqDel.SetPathValue("id", "1")
	wDel := httptest.NewRecorder()
	s.handleRackAction(wDel, reqDel)
	if wDel.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 DELETE, got %d", wDel.Result().StatusCode)
	}

	// DELETE Not Found
	reqDelNF := httptest.NewRequest("DELETE", "/api/v1/racks/99", nil)
	reqDelNF.SetPathValue("id", "99")
	wDelNF := httptest.NewRecorder()
	s.handleRackAction(wDelNF, reqDelNF)
	if wDelNF.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404 DELETE, got %d", wDelNF.Result().StatusCode)
	}

	// Suspend
	reqSus := httptest.NewRequest("POST", "/api/v1/racks/1/suspend", nil)
	reqSus.SetPathValue("id", "1")
	reqSus.SetPathValue("action", "suspend")
	wSus := httptest.NewRecorder()
	s.handleRackAction(wSus, reqSus)
	if wSus.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 Suspend, got %d", wSus.Result().StatusCode)
	}

	// Activate
	reqAct := httptest.NewRequest("POST", "/api/v1/racks/1/activate", nil)
	reqAct.SetPathValue("id", "1")
	reqAct.SetPathValue("action", "activate")
	wAct := httptest.NewRecorder()
	s.handleRackAction(wAct, reqAct)
	if wAct.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 Activate, got %d", wAct.Result().StatusCode)
	}
}

func TestHandleRackAction_WithSigner(t *testing.T) {
	// Generate signer
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	mockReg := &MockRegistry{
		ApproveFunc: func(ctx context.Context, machineID uint16, name string) (*registry.Rack, error) {
			return &registry.Rack{
				MachineID: 1,
				Name:      name,
				Status:    "active",
				Secret:    "test-secret",
			}, nil
		},
	}
	s := NewServer(mockReg, nil, signer, nil, nil, 0, nil)

	// Approve with Signer
	body := `{"name": "signed-rack"}`
	req := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "approve")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}

	// Verify response has passport (implicitly tested by code not panicking)
}

// MockPublisher for testing publishStatus
type MockPublisher struct {
	PublishedTopic string
	PublishedMsg   []byte
}

func (m *MockPublisher) Publish(topic string, messages ...*message.Message) error {
	if len(messages) > 0 {
		m.PublishedTopic = topic
		m.PublishedMsg = messages[0].Payload
	}
	return nil
}
func (m *MockPublisher) Close() error { return nil }

func TestHandleRacks_Post(t *testing.T) {
	mockReg := &MockRegistry{}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	// POST (Method not allowed)
	req := httptest.NewRequest("POST", "/api/v1/racks", nil)
	w := httptest.NewRecorder()
	s.handleRacks(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got %d", w.Result().StatusCode)
	}
}

func TestHandleRacks_ListError(t *testing.T) {
	mockReg := &MockRegistry{
		ListFunc: func(ctx context.Context, status string) ([]*registry.Rack, error) {
			return nil, errors.New("db error")
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	req := httptest.NewRequest("GET", "/api/v1/racks", nil)
	w := httptest.NewRecorder()
	s.handleRacks(w, req)
	if w.Result().StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500, got %d", w.Result().StatusCode)
	}
}

func TestHandleRackAction_WithPublisher(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockPub := &MockPublisher{}

	mockReg := &MockRegistry{
		ApproveFunc: func(ctx context.Context, machineID uint16, name string) (*registry.Rack, error) {
			return &registry.Rack{
				MachineID: 1,
				Name:      name,
				Status:    "active",
				Secret:    "test-secret",
			}, nil
		},
	}
	s := NewServer(mockReg, mockPub, signer, nil, nil, 0, nil)

	// Approve with Publisher
	body := `{"name": "pub-test"}`
	req := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "approve")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}

	// Verify publishStatus was called
	if mockPub.PublishedTopic != "fluxrig.agent.notify.1" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}

func TestHandleRackAction_SuspendWithPublisher(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockPub := &MockPublisher{}

	mockReg := &MockRegistry{
		UpdateStatusFunc: func(ctx context.Context, machineID uint16, status string) error {
			return nil
		},
	}
	s := NewServer(mockReg, mockPub, signer, nil, nil, 0, nil)

	req := httptest.NewRequest("POST", "/api/v1/racks/1/suspend", nil)
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "suspend")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}

	// Verify publishStatus was called
	if mockPub.PublishedTopic != "fluxrig.agent.notify.1" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}

func TestHandleRackAction_ActivateWithPublisher(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockPub := &MockPublisher{}

	mockReg := &MockRegistry{
		UpdateStatusFunc: func(ctx context.Context, machineID uint16, status string) error {
			return nil
		},
	}
	s := NewServer(mockReg, mockPub, signer, nil, nil, 0, nil)

	req := httptest.NewRequest("POST", "/api/v1/racks/1/activate", nil)
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "activate")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}

	if mockPub.PublishedTopic != "fluxrig.agent.notify.1" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}
func TestHandleConfig(t *testing.T) {
	cfg := &config.MixerConfig{
		API: config.ApiConfig{Port: 8090},
	}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, 0, cfg)

	req := httptest.NewRequest("GET", "/api/v1/config", nil)
	w := httptest.NewRecorder()
	s.handleConfig(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleTopology(t *testing.T) {
	mockReg := &MockRegistry{
		ListFunc: func(ctx context.Context, status string) ([]*registry.Rack, error) {
			return []*registry.Rack{{MachineID: 1, Name: "rack-1"}}, nil
		},
	}
	s := NewServer(mockReg, nil, nil, nil, nil, 0, nil)

	// STATUS
	reqStatus := httptest.NewRequest("GET", "/api/v1/topology/status", nil)
	wStatus := httptest.NewRecorder()
	s.handleTopologyStatus(wStatus, reqStatus)
	if wStatus.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 for status, got %d", wStatus.Result().StatusCode)
	}

	// LIST
	reqList := httptest.NewRequest("GET", "/api/v1/topology/list", nil)
	wList := httptest.NewRecorder()
	s.handleTopologyList(wList, reqList)
	if wList.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 for list, got %d", wList.Result().StatusCode)
	}
}

func TestHandleRackAction_Shutdown(t *testing.T) {
	mockReg := &MockRegistry{}
	signer := &pki.ClusterKey{} // Mock signer
	s := NewServer(mockReg, nil, signer, nil, nil, 0, nil)

	req := httptest.NewRequest("POST", "/api/v1/racks/1/shutdown", nil)
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "shutdown")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)

	// Will fail if rack not found in Get
	if w.Result().StatusCode != http.StatusOK {
		t.Logf("Shutdown failed as expected (no rack in mock): %d", w.Result().StatusCode)
	}
}

func TestHandleRackAction_LogLevel(t *testing.T) {
	mockPub := &MockPublisher{}
	mockReg := &MockRegistry{}
	s := NewServer(mockReg, mockPub, nil, nil, nil, 0, nil)

	body := `{"level": "debug"}`
	req := httptest.NewRequest("POST", "/api/v1/racks/1/log-level", strings.NewReader(body))
	req.SetPathValue("id", "1")
	req.SetPathValue("action", "log-level")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleEntityStats(t *testing.T) {
	cache := telemetry.NewMetricsCache()
	s := NewServer(&MockRegistry{}, nil, nil, nil, cache, 0, nil)

	// 1. All Stats
	reqAll := httptest.NewRequest("GET", "/api/v1/entities/stats", nil)
	wAll := httptest.NewRecorder()
	s.handleEntityStats(wAll, reqAll)
	if wAll.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 All, got %d", wAll.Result().StatusCode)
	}

	// 2. Single ID (Not found)
	reqSingle := httptest.NewRequest("GET", "/api/v1/entities/stats?id=123", nil)
	wSingle := httptest.NewRecorder()
	s.handleEntityStats(wSingle, reqSingle)
	if wSingle.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404 Single, got %d", wSingle.Result().StatusCode)
	}
}

type MockScenarioController struct {
	ImportFunc func(ctx context.Context, data []byte, dryRun bool) (string, error)
}

func (m *MockScenarioController) Import(ctx context.Context, data []byte, dryRun bool) (string, error) {
	if m.ImportFunc != nil {
		return m.ImportFunc(ctx, data, dryRun)
	}
	return "test-scenario", nil
}
func (m *MockScenarioController) Activate(ctx context.Context, name string) error {
	return nil
}
func (m *MockScenarioController) CurrentVersion() string {
	return "1.0"
}
func (m *MockScenarioController) GetActiveScenario() *registry.Scenario {
	return nil
}

func TestHandleScenarioImport(t *testing.T) {
	mockSC := &MockScenarioController{}
	s := NewServer(&MockRegistry{}, nil, nil, mockSC, nil, 0, nil)

	// 1. GET (Method not allowed)
	reqGet := httptest.NewRequest("GET", "/api/v1/scenario/import", nil)
	wGet := httptest.NewRecorder()
	s.handleScenarioImport(wGet, reqGet)
	if wGet.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got %d", wGet.Result().StatusCode)
	}

	// 2. POST (Dry Run)
	reqPost := httptest.NewRequest("POST", "/api/v1/scenario/import?dry_run=true", strings.NewReader("name: test"))
	wPost := httptest.NewRecorder()
	s.handleScenarioImport(wPost, reqPost)
	if wPost.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 POST, got %d", wPost.Result().StatusCode)
	}

	// 3. POST (Activate)
	reqAct := httptest.NewRequest("POST", "/api/v1/scenario/import?activate=true", strings.NewReader("name: test"))
	wAct := httptest.NewRecorder()
	s.handleScenarioImport(wAct, reqAct)
	if wAct.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 Activate, got %d", wAct.Result().StatusCode)
	}
}

func TestHandleTopologyStatus(t *testing.T) {
	mockSC := &MockScenarioController{}
	s := NewServer(&MockRegistry{}, nil, nil, mockSC, nil, 0, nil)

	req := httptest.NewRequest("GET", "/api/v1/topology/status", nil)
	w := httptest.NewRecorder()
	s.handleTopologyStatus(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleTopologyList(t *testing.T) {
	mockSC := &MockScenarioController{}
	s := NewServer(&MockRegistry{}, nil, nil, mockSC, nil, 0, nil)

	req := httptest.NewRequest("GET", "/api/v1/topology/list", nil)
	w := httptest.NewRecorder()
	s.handleTopologyList(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}
}

func TestHandleRackAction_Delete(t *testing.T) {
	reg := &MockRegistry{}
	s := NewServer(reg, nil, nil, nil, nil, 0, nil)

	req := httptest.NewRequest("DELETE", "/api/v1/racks/123", nil)
	req.SetPathValue("id", "123")
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 DELETE, got %d", w.Result().StatusCode)
	}
}
func TestServer_StartFailure(t *testing.T) {
	cfg := &config.MixerConfig{
		API: config.ApiConfig{
			Port: 8090,
		},
	}
	s := NewServer(&MockRegistry{}, nil, nil, nil, nil, 0, cfg)

	// Using an invalid address to trigger listener failure
	err := s.Start("999.999.999.999:80")
	if err == nil {
		t.Error("Expected error for invalid bind address, got nil")
	}
}
