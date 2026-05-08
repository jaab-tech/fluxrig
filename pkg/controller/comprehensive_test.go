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

	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/router"
)

type MockRegComp struct {
	RegisterFunc  func(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error)
	HeartbeatFunc func(ctx context.Context, id uuid.UUID, stats map[string]any) error
	GetFunc       func(ctx context.Context, id uuid.UUID) (*registry.Rack, error)

	registry.Registry
}

func (m *MockRegComp) Register(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, machineID, name, secret, ip, port, version, config, mixerID)
	}
	return &registry.Rack{MachineID: uuid.New(), Name: name, Status: "active"}, nil
}
func (m *MockRegComp) Heartbeat(ctx context.Context, machineID uuid.UUID, stats map[string]any, attrs map[string]any) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, machineID, stats)
	}
	return nil
}
func (m *MockRegComp) Get(ctx context.Context, machineID uuid.UUID) (*registry.Rack, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, machineID)
	}
	return &registry.Rack{MachineID: machineID, Status: "active"}, nil
}

func (m *MockRegComp) Approve(ctx context.Context, machineID uuid.UUID, newName string) (*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegComp) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegComp) Remove(ctx context.Context, machineID uuid.UUID) error { return nil }
func (m *MockRegComp) UpdateStatus(ctx context.Context, machineID uuid.UUID, status string) error {
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
		RegisterFunc: func(ctx context.Context, machineID uuid.UUID, name, secret, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
			callCount++
			return &registry.Rack{MachineID: uuid.New(), Name: name, Status: "active"}, nil
		},
	}
	mockPub := &MockPubComp{}

	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), time.Second)

	hello := &fluxmsg.HelloPayload{Name: "dedup-rack", IP: "1.1.1.1", Port: 1234, Version: "v1"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := cbor.Marshal(fm)
	msg := message.NewMessage("1", raw)

	_, _ = ctrl.HandleHello(msg)
	_, _ = ctrl.HandleHello(msg)

	if callCount != 1 {
		t.Errorf("Expected 1 register call (deduped), got %d", callCount)
	}
}

func TestEnrollment_RegistryError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	mockReg := &MockRegComp{
		RegisterFunc: func(ctx context.Context, machineID uuid.UUID, name, secret, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
			return nil, errors.New("db error")
		},
	}
	mockPub := &MockPubComp{}
	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), time.Second)

	hello := &fluxmsg.HelloPayload{Name: "err-rack"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := cbor.Marshal(fm)
	msg := message.NewMessage("1", raw)

	_, err := ctrl.HandleHello(msg)
	if err == nil {
		t.Error("Expected error from HandleHello on DB failure")
	}
	if len(mockPub.CapturedMessages) > 0 {
		t.Error("Should not publish passport if registration fails")
	}
}

func TestEnrollment_PublisherError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{Fail: true}
	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), time.Second)

	hello := &fluxmsg.HelloPayload{Name: "pub-err-rack"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := cbor.Marshal(fm)
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

	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), 10*time.Millisecond)
	ctrl.SetScenario(mockScen)

	hello := &fluxmsg.HelloPayload{Name: "scen-rack", IP: "1.1.1.1", Port: 80, Version: "v1"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := cbor.Marshal(fm)
	msg := message.NewMessage("1", raw)

	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	mockScen.mu.Lock()
	called := mockScen.PushCalled
	mockScen.mu.Unlock()

	if !called {
		t.Error("PushActiveToRack was not called")
	}
}

func TestEnrollment_RegisterRoutes(t *testing.T) {
	mockReg := &MockRegComp{}
	mockPub := &MockPubComp{}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}

	ctrl := controller.NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), time.Second)

	wmRouter, err := message.NewRouter(message.RouterConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &router.RouterWrapper{
		Router: wmRouter,
		Sub:    nil,
	}

	ctrl.RegisterRoutes(wrapper)
}
