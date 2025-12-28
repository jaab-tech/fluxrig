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
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// MockRegistry for testing
type MockRegistry struct {
	ListFunc        func(ctx context.Context, status string) ([]*registry.Rack, error)
	ApproveFunc     func(ctx context.Context, machineID uint16, name string) (*registry.Rack, error)
	CalledHeartbeat bool
	// Add helpers for extended tests
	RemoveFunc       func(ctx context.Context, machineID uint16) error
	UpdateStatusFunc func(ctx context.Context, machineID uint16, status string) error
}

func (m *MockRegistry) Register(ctx context.Context, name string, secret string, ip string, port int, version string) (*registry.Rack, error) {
	return nil, nil // Not used in API server tests yet
}
func (m *MockRegistry) Get(ctx context.Context, machineID uint16) (*registry.Rack, error) {
	// For testing Passport generation, we need to return a rack
	if machineID == 1 {
		return &registry.Rack{MachineID: 1, Name: "rack-1", Status: "active"}, nil
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

func (m *MockRegistry) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any) error {
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
	return nil, nil
}
func (m *MockRegistry) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	return nil, nil
}

func TestHandleHealth(t *testing.T) {
	s := NewServer(&MockRegistry{}, nil, nil)
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
	s := NewServer(mockReg, nil, nil)

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
	s := NewServer(mockReg, nil, nil)

	// 1. Invalid Path (No ID)
	req1 := httptest.NewRequest("POST", "/api/v1/racks/", nil) // Trailing slash stripped effectively?
	w1 := httptest.NewRecorder()
	s.handleRackAction(w1, req1)
	if w1.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 for empty ID, got %d", w1.Result().StatusCode)
	}

	// 2. Approve Success
	body := `{"name": "new-name"}`
	req2 := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	s.handleRackAction(w2, req2)
	if w2.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w2.Result().StatusCode)
	}

	// 3. Not Found
	req3 := httptest.NewRequest("POST", "/api/v1/racks/99/approve", strings.NewReader(body))
	w3 := httptest.NewRecorder()
	s.handleRackAction(w3, req3)
	if w3.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", w3.Result().StatusCode)
	}

	// 4. Invalid JSON
	req4 := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader("bad"))
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
	s := NewServer(mockReg, nil, nil)

	// DELETE
	reqDel := httptest.NewRequest("DELETE", "/api/v1/racks/1", nil)
	wDel := httptest.NewRecorder()
	s.handleRackAction(wDel, reqDel)
	if wDel.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 DELETE, got %d", wDel.Result().StatusCode)
	}

	// DELETE Not Found
	reqDelNF := httptest.NewRequest("DELETE", "/api/v1/racks/99", nil)
	wDelNF := httptest.NewRecorder()
	s.handleRackAction(wDelNF, reqDelNF)
	if wDelNF.Result().StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404 DELETE, got %d", wDelNF.Result().StatusCode)
	}

	// Suspend
	reqSus := httptest.NewRequest("POST", "/api/v1/racks/1/suspend", nil)
	wSus := httptest.NewRecorder()
	s.handleRackAction(wSus, reqSus)
	if wSus.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 Suspend, got %d", wSus.Result().StatusCode)
	}

	// Activate
	reqAct := httptest.NewRequest("POST", "/api/v1/racks/1/activate", nil)
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
	s := NewServer(mockReg, nil, signer)

	// Approve with Signer
	body := `{"name": "signed-rack"}`
	req := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
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
	s := NewServer(mockReg, nil, nil)

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
	s := NewServer(mockReg, nil, nil)

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
	s := NewServer(mockReg, mockPub, signer)

	// Approve with Publisher
	body := `{"name": "pub-test"}`
	req := httptest.NewRequest("POST", "/api/v1/racks/1/approve", strings.NewReader(body))
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
	s := NewServer(mockReg, mockPub, signer)

	req := httptest.NewRequest("POST", "/api/v1/racks/1/suspend", nil)
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
	s := NewServer(mockReg, mockPub, signer)

	req := httptest.NewRequest("POST", "/api/v1/racks/1/activate", nil)
	w := httptest.NewRecorder()
	s.handleRackAction(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Result().StatusCode)
	}

	if mockPub.PublishedTopic != "fluxrig.agent.notify.1" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}
