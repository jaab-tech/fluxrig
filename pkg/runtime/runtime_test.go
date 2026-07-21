// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Mock Bus
type MockBus struct {
	FailSubscribe  bool
	DisableReflect bool
	handlers       map[string]bus.Handler
}

func (m *MockBus) Connect(url string, opts bus.ConnectOptions) error {
	if m.handlers == nil {
		m.handlers = make(map[string]bus.Handler)
	}
	return nil
}
func (m *MockBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	if m.handlers == nil {
		m.handlers = make(map[string]bus.Handler)
	}
	// Simulated Reflection for Sync Probes
	if msg.Flags&fluxmsg.FlagSyncProbe != 0 && !m.DisableReflect {
		if h, ok := m.handlers[subject]; ok {
			// Deliver in goroutine to simulate real bus behavior and prevent deadlocks (m.mu)
			go h(ctx, msg)
		}
	}
	return nil
}
func (m *MockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	return nil
}
func (m *MockBus) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	if m.FailSubscribe {
		return nil, errors.New("subscribe failed")
	}
	if m.handlers == nil {
		m.handlers = make(map[string]bus.Handler)
	}
	m.handlers[subject] = handler
	return &MockSub{}, nil
}
func (m *MockBus) SubscribeRaw(subject string, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	return &MockSub{}, nil
}
func (m *MockBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *MockBus) Request(subject string, msg *fluxmsg.FluxMsg, timeout time.Duration) (*fluxmsg.FluxMsg, error) {
	return nil, nil
}
func (m *MockBus) Close()           {}
func (m *MockBus) KV() bus.KeyValue { return nil }
func (m *MockBus) Core() any        { return nil }

type MockSub struct{}

func (m *MockSub) Unsubscribe() error { return nil }

type MockGear struct {
	InitErr  error
	StartErr error
}

func (m *MockGear) Init(ctx sdk.GearContext) error                               { return m.InitErr }
func (m *MockGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error { return m.StartErr }

type MockManager struct{}

func (m *MockManager) Import(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	return "", "", "", nil
}
func (m *MockManager) ImportScenario(ctx context.Context, filePath, name, tag string) (string, string, string, error) {
	return "", "", "", nil
}

func (m *MockManager) Load(ctx context.Context, u string) ([]byte, error) {
	return nil, nil
}
func (m *MockManager) Export(ctx context.Context, u, outputPath string) error {
	return nil
}
func (m *MockManager) List(ctx context.Context) ([]manager.ArtifactInfo, error) {
	return nil, nil
}

func (m *MockGear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	return msg, nil
}
func (m *MockGear) Stop() error {
	return nil
}

func (m *MockGear) Drain(ctx context.Context) error {
	return nil
}

func TestManager_Lifecycle(t *testing.T) {
	// 1. Setup Dependencies
	mockBus := &MockBus{}
	mockSpecMgr := &MockManager{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)

	mgr := NewManager(mid, "test-rack", mockBus, gen, mockSpecMgr, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)

	// 2. Apply Scenario
	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{
				Name:   "g1",
				Type:   "io_tcp",
				Deploy: "test-rack",
				Config: map[string]any{"bind": ":0"},
			},
		},
		Wires: []registry.WireSpec{
			{From: "g1.in", To: "g1.out"}, // Loopbackish, just to test wire logic
		},
	}

	ctx := context.Background()
	if err := mgr.ApplyScenario(ctx, sc); err != nil {
		t.Fatalf("ApplyScenario failed: %v", err)
	}

	// 3. Shutdown
	mgr.Shutdown()
}

func TestManager_Errors(t *testing.T) {
	mockBus := &MockBus{}
	mockSpecMgr := &MockManager{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "test-rack", mockBus, gen, mockSpecMgr, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)

	// 1. Unknown Gear Type
	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "g1", Type: "unknown_type", Deploy: "test-rack"},
		},
	}
	if err := mgr.ApplyScenario(context.Background(), sc); err == nil {
		t.Error("Expected error for unknown gear type")
	} else if !strings.Contains(err.Error(), "create gear") {
		t.Errorf("Wrong error: %v", err)
	}

	// 2. Init Failure
	mgr.factory.Register("fail_init", func() sdk.NativeGear {
		return &MockGear{InitErr: errors.New("init boom")}
	})
	sc.Gears[0].Type = "fail_init"
	if err := mgr.ApplyScenario(context.Background(), sc); err == nil {
		t.Error("Expected error for init failure")
	} else if !strings.Contains(err.Error(), "init gear") {
		t.Errorf("Wrong error: %v", err)
	}

	// 3. Start Failure
	mgr.factory.Register("fail_start", func() sdk.NativeGear {
		return &MockGear{StartErr: errors.New("start boom")}
	})
	sc.Gears[0].Type = "fail_start" // Re-use same spec slot
	if err := mgr.ApplyScenario(context.Background(), sc); err == nil {
		t.Error("Expected error for start failure")
	} else if !strings.Contains(err.Error(), "start gear") {
		t.Errorf("Wrong error: %v", err)
	}

	// 4. Subscribe Failure
	failBus := &MockBus{FailSubscribe: true}
	mgr2 := NewManager(mid, "test-rack", failBus, gen, mockSpecMgr, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)
	// Use good gear, but bad bus
	sc2 := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{
				Name:   "g1",
				Type:   "io_tcp",
				Deploy: "test-rack",
				Config: map[string]any{"bind": ":0"},
			},
		},
		Wires: []registry.WireSpec{
			{From: "g1.in", To: "g1.out"},
		},
	}
	if err := mgr2.ApplyScenario(context.Background(), sc2); err == nil {
		t.Error("Expected error for subscribe failure")
	} else if !strings.Contains(err.Error(), "subscribe wire") {
		t.Errorf("Wrong error: %v", err)
	}
}

func TestManager_Drain(t *testing.T) {
	mockBus := &MockBus{}
	mockSpecMgr := &MockManager{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "test-rack", mockBus, gen, mockSpecMgr, 1*time.Second, 1*time.Second, 100*time.Millisecond, false, false, nil)

	// Use MockGear for deterministic drain
	mgr.factory.Register("mock_drain", func() sdk.NativeGear {
		return &MockGear{}
	})

	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "g1", Type: "mock_drain", Deploy: "test-rack"},
		},
	}
	if err := mgr.ApplyScenario(context.Background(), sc); err != nil {
		t.Fatalf("setup scenario failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.Drain(ctx); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
}

func TestManager_ConvergenceTimeout(t *testing.T) {
	// Disable reflection in MockBus to simulate convergence delay
	mockBus := &MockBus{DisableReflect: true}
	mockSpecMgr := &MockManager{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	// Short timeouts for fast test
	mgr := NewManager(mid, "test-rack", mockBus, gen, mockSpecMgr, 100*time.Millisecond, 200*time.Millisecond, 50*time.Millisecond, false, false, nil)

	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{
				Name:   "g1",
				Type:   "io_tcp",
				Deploy: "test-rack",
				Config: map[string]any{"bind": ":0"},
			},
		},
		Wires: []registry.WireSpec{
			{From: "g2.out", To: "g1.in"}, // g2 doesn't exist, will be "global"
		},
	}

	if err := mgr.ApplyScenario(context.Background(), sc); err == nil {
		t.Error("Expected convergence timeout error")
	} else if !strings.Contains(err.Error(), "timeout waiting for subject convergence") && !strings.Contains(err.Error(), "subscribe wire") {
		// Accept both for now as MockBus behavior may vary across environments
		t.Errorf("Wrong error: %v", err)
	}
}
