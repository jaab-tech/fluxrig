// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"log/slog"
	"time"
)

// Mock Registry
type MockRegistry struct {
	RegisterFunc  func(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*registry.Rack, error)
	HeartbeatFunc func(ctx context.Context, machineID uint16, stats map[string]any) error
	GetFunc       func(ctx context.Context, machineID uint16) (*registry.Rack, error)
}

func (m *MockRegistry) Register(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*registry.Rack, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, name, secret, ip, port, version, config, mixerID) // Mapper
	}
	return &registry.Rack{MachineID: 1, Name: name, Status: "active", Secret: "test-secret"}, nil
}

func (m *MockRegistry) Get(ctx context.Context, machineID uint16) (*registry.Rack, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, machineID)
	}
	return &registry.Rack{MachineID: machineID, Name: "test-rack", Status: "active"}, nil
}

func (m *MockRegistry) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegistry) Approve(ctx context.Context, machineID uint16, name string) (*registry.Rack, error) {
	return nil, nil
}
func (m *MockRegistry) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any, attrs map[string]any) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, machineID, stats)
	}
	return nil
}
func (m *MockRegistry) Remove(ctx context.Context, machineID uint16) error {
	return nil
}
func (m *MockRegistry) UpdateStatus(ctx context.Context, machineID uint16, status string) error {
	return nil
}
func (m *MockRegistry) SetAutoAdopt(enabled bool) {}
func (m *MockRegistry) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) {
	return nil, nil
}
func (m *MockRegistry) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) {
	return nil, nil
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
	// Setup
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	// Create Hello Message
	hello := &fluxmsg.HelloPayload{
		Name:      "test-rack",
		MachineID: 0, // New rack
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

	// Execute
	_, err := ctrl.HandleHello(msg)
	if err != nil {
		t.Fatalf("HandleHello failed: %v", err)
	}

	// Verify
	if mockPub.PublishedTopic != "fluxrig.agent.enrollment.test-rack.test-nonce" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
	if mockPub.PublishedMessage == nil {
		t.Error("No message published")
	}
}

func TestEnrollmentController_HandleHeartbeat(t *testing.T) {
	// Setup
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	// Create Heartbeat Message
	hb := &fluxmsg.HeartbeatPayload{
		MachineID: 42,
		Stats:     map[string]any{"cpu": 50},
	}
	hbData, _ := hb.ToData()

	fm := fluxmsg.New()
	fm.Data = hbData
	payload, _ := cbor.Marshal(fm)

	msg := message.NewMessage("test-uuid", payload)

	// Execute
	_, err := ctrl.HandleHeartbeat(msg)
	if err != nil {
		t.Fatalf("HandleHeartbeat failed: %v", err)
	}

	// Verify
	if mockPub.PublishedTopic != "fluxrig.agent.notify.42" {
		t.Errorf("Wrong topic: %s", mockPub.PublishedTopic)
	}
}

func TestEnrollmentController_Garbage(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: priv, Public: pub}
	mockReg := &MockRegistry{}
	mockPub := &MockPublisher{}

	// Must provide signer to avoid panic if validation mistakenly passes
	ctrl := NewEnrollmentController(slog.Default(), mockReg, mockPub, signer, 0x0200010000000001, time.Second)

	// Test 1: Malformed CBOR
	msg := message.NewMessage("test", []byte("garbage"))
	resp, err := ctrl.HandleHello(msg)
	if resp != nil || err != nil {
		t.Error("Expected nil/nil for garbage hello")
	}

	// Test 2: Malformed Hello Payload
	fm := fluxmsg.New()
	fm.Data = map[string]any{"wrong": "field"}
	payload, _ := cbor.Marshal(fm)
	msg2 := message.NewMessage("test", payload)

	resp, err = ctrl.HandleHello(msg2)
	if resp != nil || err != nil {
		t.Error("Expected nil/nil for invalid hello payload")
	}
}
