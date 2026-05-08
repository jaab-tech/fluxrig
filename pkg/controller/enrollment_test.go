// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Mock Registry
type MockRegistry struct {
	RegisterFunc  func(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error)
	HeartbeatFunc func(ctx context.Context, machineID uuid.UUID, stats map[string]any) error
	GetFunc       func(ctx context.Context, machineID uuid.UUID) (*registry.Rack, error)
}

func (m *MockRegistry) Register(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, machineID, name, secret, ip, port, version, config, mixerID)
	}
	return &registry.Rack{MachineID: uuid.New(), Name: name, Status: "active", Secret: "test-secret"}, nil
}

func (m *MockRegistry) RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	return nil, nil
}

func (m *MockRegistry) Get(ctx context.Context, machineID uuid.UUID) (*registry.Rack, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, machineID)
	}
	return &registry.Rack{MachineID: machineID, Name: "test-rack", Status: "active"}, nil
}

func (m *MockRegistry) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegistry) Approve(ctx context.Context, machineID uuid.UUID, name string) (*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegistry) Heartbeat(ctx context.Context, machineID uuid.UUID, stats map[string]any, attrs map[string]any) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, machineID, stats)
	}
	return nil
}
func (m *MockRegistry) HeartbeatEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, stats map[string]any, config map[string]any) error {
	return nil
}
func (m *MockRegistry) Remove(ctx context.Context, machineID uuid.UUID) error { return nil }
func (m *MockRegistry) RemoveByName(ctx context.Context, name string) error   { return nil }
func (m *MockRegistry) RemoveEntity(ctx context.Context, typeID uint8, machineID uuid.UUID) error {
	return nil
}
func (m *MockRegistry) UpdateStatus(ctx context.Context, machineID uuid.UUID, status string) error {
	return nil
}
func (m *MockRegistry) UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error {
	return nil
}
func (m *MockRegistry) SetAutoAdopt(enabled bool) {}
func (m *MockRegistry) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) {
	return nil, nil
}
func (m *MockRegistry) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	return nil, nil
}
func (m *MockRegistry) ClearScenarioEntities(ctx context.Context) error {
	return nil
}

// Mock Publisher
type MockPublisher struct {
	PublishedTopic   string
	PublishedMessage *message.Message
}

func (m *MockPublisher) Publish(topic string, messages ...*message.Message) error {
	if len(messages) > 0 {
		m.PublishedTopic = topic
		m.PublishedMessage = messages[0]
	}
	return nil
}
func (m *MockPublisher) Close() error { return nil }

func TestEnrollmentController_HandleHello(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	mixerID := uuid.New()
	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, mixerID, time.Second)

	hello := &fluxmsg.HelloPayload{
		Name:      "test-rack",
		MachineID: uuid.Nil,
		IP:        "10.0.0.1",
		Port:      8080,
		Secret:    "",
		Nonce:     "test-nonce",
	}
	helloData, _ := hello.ToData()

	fm := fluxmsg.New()
	fm.Data = helloData
	payload, _ := cbor.Marshal(fm)

	msg := message.NewMessage("test-uuid", payload)

	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}

	if mockPub.PublishedTopic != "flux.agent.enrollment.test-rack.test-nonce" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}

func TestEnrollmentController_HandleHeartbeat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	mixerID := uuid.New()
	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, mixerID, time.Second)

	machineID := uuid.New()
	hb := &fluxmsg.HeartbeatPayload{
		MachineID: machineID,
		Stats:     map[string]any{"cpu": 50},
	}
	hbData, _ := hb.ToData()

	fm := fluxmsg.New()
	fm.Data = hbData
	payload, _ := cbor.Marshal(fm)

	msg := message.NewMessage("test-uuid", payload)

	_, err := ctrl.HandleHeartbeat(msg)
	if err != nil {
		t.Fatalf("HandleHeartbeat failed: %v", err)
	}

	expectedTopic := "flux.agent.notify." + machineID.String()
	if mockPub.PublishedTopic != expectedTopic {
		t.Errorf("Wrong topic: got %s, want %s", mockPub.PublishedTopic, expectedTopic)
	}
}

func TestEnrollmentController_Garbage(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, uuid.New(), time.Second)

	msg := message.NewMessage("test", []byte("garbage"))
	resp, err := ctrl.HandleHello(msg)
	if resp != nil || err != nil {
		t.Error("Expected nil/nil for garbage hello")
	}

	fm := fluxmsg.New()
	fm.Data = map[string]any{"wrong": "field"}
	payload, _ := cbor.Marshal(fm)
	msg2 := message.NewMessage("test", payload)

	resp, err = ctrl.HandleHello(msg2)
	if resp != nil || err != nil {
		t.Error("Expected nil/nil for invalid hello payload")
	}
}
