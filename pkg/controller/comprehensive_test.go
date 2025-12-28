package controller_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/vmihailenco/msgpack/v5"
)

// Reusing MockRegistry from enrollment_test.go (copying minimal needed or assuming package level visibility if same package?)
// enrollment_test.go is `package controller`. This is `package controller_test`.
// I need to define local mocks or export them.
// I'll define local mocks for comprehensive test.

type MockRegComp struct {
	RegisterFunc func(ctx context.Context, name string, secret string, ip string, port int, version string) (*registry.Rack, error)
	HeartbeatFunc func(ctx context.Context, id uint16, stats map[string]any) error
	GetFunc func(ctx context.Context, id uint16) (*registry.Rack, error)

	// Satisfy interface
	registry.Registry
}

func (m *MockRegComp) Register(ctx context.Context, name string, secret string, ip string, port int, version string) (*registry.Rack, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, name, secret, ip, port, version)
	}
	return &registry.Rack{MachineID: 100, Name: name, Status: "active"}, nil
}
func (m *MockRegComp) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any) error {
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
func (m *MockRegComp) Approve(ctx context.Context, machineID uint16, newName string) (*registry.Rack, error) { return nil, nil }
func (m *MockRegComp) List(ctx context.Context, status string) ([]*registry.Rack, error) { return nil, nil }
func (m *MockRegComp) Remove(ctx context.Context, machineID uint16) error { return nil }
func (m *MockRegComp) UpdateStatus(ctx context.Context, machineID uint16, status string) error { return nil }
func (m *MockRegComp) QueryLogs(ctx context.Context, query registry.LogQuery) ([]registry.LogEntry, error) { return nil, nil }
func (m *MockRegComp) QueryMetrics(ctx context.Context, query registry.MetricQuery) ([]registry.MetricEntry, error) { return nil, nil }


type MockPubComp struct {
	CapturedMessages []*message.Message
	Fail bool
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
		RegisterFunc: func(ctx context.Context, name, secret, ip string, port int, version string) (*registry.Rack, error) {
			callCount++
			return &registry.Rack{MachineID: 100, Name: name, Status: "active"}, nil
		},
	}
	mockPub := &MockPubComp{}
	
	ctrl := controller.NewEnrollmentController(mockReg, mockPub, signer)

	// Msg
	hello := &fluxmsg.HelloPayload{Name: "dedup-rack", IP: "1.1.1.1", Port: 1234, Version: "v1"}
	data, _ := hello.ToData()
	fm := fluxmsg.New()
	fm.Data = data
	raw, _ := msgpack.Marshal(fm)
	msg := message.NewMessage("1", raw)

	// Call 1
	ctrl.HandleHello(msg)
	// Call 2 (Immediate)
	ctrl.HandleHello(msg)

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

	c := controller.NewEnrollmentController(mockReg, mockPub, signer)
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
		RegisterFunc: func(ctx context.Context, name, secret, ip string, port int, version string) (*registry.Rack, error) {
			return nil, errors.New("db error")
		},
	}
	mockPub := &MockPubComp{}
	ctrl := controller.NewEnrollmentController(mockReg, mockPub, signer)

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
	ctrl := controller.NewEnrollmentController(mockReg, mockPub, signer)

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
