// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/vmihailenco/msgpack/v5"
	"log/slog"
	"time"
)

// Reusing MockRegistry from enrollment_test.go (copying minimal needed or assuming package level visibility if same package?)
// enrollment_test.go is `package controller`. This is `package controller_test`.
// I need to define local mocks or export them.
// I'll define local mocks for comprehensive test.

type MockRegComp struct {
	RegisterFunc  func(ctx context.Context, name string, secret string, ip string, port int, version string, mixerID uint64) (*registry.Rack, error)
	HeartbeatFunc func(ctx context.Context, id uint16, stats map[string]any) error
	GetFunc       func(ctx context.Context, id uint16) (*registry.Rack, error)

	// Satisfy interface
	registry.Registry
}

func (m *MockRegComp) Register(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*registry.Rack, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, name, secret, ip, port, version, mixerID)
	}
	return &registry.Rack{MachineID: 100, Name: name, Status: "active"}, nil
}
func (m *MockRegComp) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any, attrs map[string]any) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, machineID, stats)
	}
	return nil
}
func (m *MockRegComp) Get(ctx context.Context, machineID uint16) (*registry.Rack, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, machineID)
	}
	return &registry.Rack{MachineID: machineID, Status: "active"}, nil
}

// Other methods needed for interface? Yes.
func (m *MockRegComp) Approve(ctx context.Context, machineID uint16, newName string) (*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegComp) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegComp) Remove(ctx context.Context, machineID uint16) error { return nil }
func (m *MockRegComp) UpdateStatus(ctx context.Context, machineID uint16, status string) error {
	return nil
}
func (m *MockRegComp) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) {
	return nil, nil
}
func (m *MockRegComp) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	return nil, nil
}

type MockPubComp struct {
	CapturedMessages []*message.Message
	Fail             bool
}

func (m *MockPubComp) Publish(topic string, messages ...*message.Message) error {
	if m.Fail {
		return errors.New("publisher failed")
	}
	m.CapturedMessages = append(m.CapturedMessages, messages...)
	return nil
}
func (m *MockPubComp) Close() error { return nil }

func TestEnrollment_Deduplication(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	callCount := 0
	mockReg := &MockRegComp{
		RegisterFunc: func(ctx context.Context, name, secret, ip string, port int, version string, mixerID uint64) (*registry.Rack, error) {
			callCount++
			return &registry.Rack{MachineID: 100, Name: name, Status: "active"}, nil
		},
	}
	mockPub := &MockPubComp{}

	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	// Msg
	hello := &fluxmsg.HelloPayload{Name: "dedup-rack", IP: "1.1.1.1", Port: 1234, Version: "v1"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := msgpack.Marshal(fm)
	msg := message.NewMessage("1", raw)

	// Call 1
	_, _ = ctrl.HandleHello(msg)
	// Call 2 (Immediate)
	_, _ = ctrl.HandleHello(msg)

	if callCount != 1 {
		t.Errorf("Expected 1 register call (deduped), got %d", callCount)
	}
}

func TestEnrollmentController_RegisterRoutes(t *testing.T) {
	// For this test, we need a mock registry and publisher, but the actual RegisterRoutes
	// method doesn't use them directly, it just sets up handlers.
	// We pass nil for simplicity as the method signature requires them,
	// but they aren't dereferenced in RegisterRoutes itself.
	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	c := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)
	// We can't easily test RegisterRoutes without a real RouterWrapper
	// But we can verify the controller is valid
	if c == nil {
		t.Error("NewEnrollmentController returned nil")
	}
	// A more thorough test would involve a mock watermill router and verifying
	// that the expected topics are subscribed to and handlers are registered.
	// For now, just ensuring the controller can be created is a basic check.
}

func TestEnrollment_RegistryError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	mockReg := &MockRegComp{
		RegisterFunc: func(ctx context.Context, name, secret, ip string, port int, version string, mixerID uint64) (*registry.Rack, error) {
			return nil, errors.New("db error")
		},
	}
	mockPub := &MockPubComp{}
	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	hello := &fluxmsg.HelloPayload{Name: "err-rack"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := msgpack.Marshal(fm)
	msg := message.NewMessage("1", raw)

	resp, err := ctrl.HandleHello(msg)
	if err == nil {
		// handleHello returns error if DB fails
		t.Error("Expected error from HandleHello on DB failure")
	}
	if len(mockPub.CapturedMessages) > 0 {
		t.Error("Should not publish passport if registration fails")
	}
	_ = resp
}

func TestEnrollment_PublisherError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{Fail: true}
	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	hello := &fluxmsg.HelloPayload{Name: "pub-err-rack"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := msgpack.Marshal(fm)
	msg := message.NewMessage("1", raw)

	_, err := ctrl.HandleHello(msg)
	if err == nil {
		t.Error("Expected error from HandleHello on Publisher failure")
	}
}

type MockScenarioProvider struct {
	PushFunc   func(ctx context.Context, rackName string) error
	PushCalled bool
	mu         sync.Mutex
}

func (m *MockScenarioProvider) PushActiveToRack(ctx context.Context, rackName string) error {
	m.mu.Lock()
	m.PushCalled = true
	m.mu.Unlock()
	if m.PushFunc != nil {
		return m.PushFunc(ctx, rackName)
	}
	return nil
}

func TestEnrollment_ScenarioPush(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{}
	mockScen := &MockScenarioProvider{
		PushFunc: func(ctx context.Context, rackName string) error {
			if rackName != "scen-rack" {
				t.Errorf("Expected rack name scen-rack, got %s", rackName)
			}
			return nil
		},
	}

	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, 10*time.Millisecond)
	ctrl.SetScenario(mockScen)

	hello := &fluxmsg.HelloPayload{Name: "scen-rack", IP: "1.1.1.1", Port: 80, Version: "v1"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := msgpack.Marshal(fm)
	msg := message.NewMessage("1", raw)

	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}

	// Wait for async push
	time.Sleep(50 * time.Millisecond)

	mockScen.mu.Lock()
	called := mockScen.PushCalled
	mockScen.mu.Unlock()

	if !called {
		t.Error("PushActiveToRack was not called")
	}
}

// Mock Router Helper
type MockRouter struct {
	Handlers map[string]string // topic -> handlerName
}

func (m *MockRouter) AddHandler(name, topic, subTitle string, pubTitle string, pub message.Publisher, handlerFunc message.NoPublishHandlerFunc) *message.Handler {
	if m.Handlers == nil {
		m.Handlers = make(map[string]string)
	}
	m.Handlers[topic] = name
	return nil
}

func TestEnrollment_RegisterRoutes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{}
	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	// To test RegisterRoutes properly we'd need to mock router.RouterWrapper which wraps watermill.Router.
	// Since RouterWrapper is a struct in another package, we can't easily interface-mock it unless we change the signature.
	// But `RegisterRoutes` takes `*router.RouterWrapper`. If we can't mock it, we verify what we can.
	// Wait, the test I saw earlier `TestEnrollmentController_RegisterRoutes` was basically empty.
	// If we can't easily mock the router wrapper struct methods, we might have to skip deep verification
	// or rely on integration tests.
	// However, we can check if it PANICS or runs.

	// Actually, `RegisterRoutes` calls `r.Router.AddHandler`. `r.Router` is `*message.Router`.
	// We can create a real watermill router and pass it?

	wmRouter, err := message.NewRouter(message.RouterConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &router.RouterWrapper{
		Router: wmRouter,
		Sub:    nil,
	}

	// RegisterRoutes shouldn't panic
	ctrl.RegisterRoutes(wrapper)
}
