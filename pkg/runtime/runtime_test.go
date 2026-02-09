// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Mock Bus
type MockBus struct {
	FailSubscribe bool
}

func (m *MockBus) Connect(url string, opts bus.ConnectOptions) error {
	return nil
}
func (m *MockBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	return nil
}
func (m *MockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uint64) error {
	return nil
}
func (m *MockBus) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	if m.FailSubscribe {
		return nil, errors.New("subscribe failed")
	}
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

type MockSub struct{}

func (m *MockSub) Unsubscribe() error { return nil }

type MockGear struct {
	InitErr  error
	StartErr error
}

func (m *MockGear) Init(ctx sdk.GearContext) error                               { return m.InitErr }
func (m *MockGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error { return m.StartErr }
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
	gen, _ := idgen.New(1)

	mgr := NewManager(slog.Default(), mockBus, gen, 100, "test-rack", 5*time.Second)

	// 2. Apply Scenario
	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "g1", Type: "io_tcp", Deploy: "test-rack"},
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
	gen, _ := idgen.New(1)
	mgr := NewManager(slog.Default(), mockBus, gen, 100, "test-rack", 5*time.Second)

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
	mgr2 := NewManager(slog.Default(), failBus, gen, 100, "test-rack", 5*time.Second)
	// Use good gear, but bad bus
	sc2 := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "test", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "g1", Type: "io_tcp", Deploy: "test-rack"},
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
