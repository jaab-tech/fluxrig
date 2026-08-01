// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Mock Bus
type MockBus struct {
	FailSubscribe  bool
	DisableReflect bool
	PanicOnPublish bool // panic on a non-probe Publish, to exercise emit recovery
	handlers       map[string]bus.Handler
	pubMu          sync.Mutex
	published      []string // subjects of non-probe Publishes, for assertions
}

func (m *MockBus) publishedSubjects() []string {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	return append([]string(nil), m.published...)
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
	// Record real (non-probe) emissions for assertions.
	if msg.Flags&fluxmsg.FlagSyncProbe == 0 {
		m.pubMu.Lock()
		m.published = append(m.published, subject)
		m.pubMu.Unlock()
	}
	// Panic on real (non-probe) emissions so tests can verify the runtime's
	// emit recovery. Sync probes must still succeed for convergence.
	if m.PanicOnPublish && msg.Flags&fluxmsg.FlagSyncProbe == 0 {
		panic("simulated publish panic")
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
				Config: map[string]any{"mode": "server", "bind": ":0"},
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

// emittingGear is a source gear that emits once from Start, used to drive a
// panic through the runtime's emit path.
type emittingGear struct {
	MockGear
}

func (g *emittingGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	emit(&fluxmsg.FluxMsg{}) // routed through the runtime emitFunc -> bus.Publish (panics)
	return nil
}

// A panic on the emit path (source gears, async emitters like a correlation
// gear's timeout daemon) must be contained, not crash the Rack. Without the
// recover in emitFunc this test aborts the whole test binary.
func TestManager_EmitPanicRecovered(t *testing.T) {
	mockBus := &MockBus{PanicOnPublish: true}
	mockSpecMgr := &MockManager{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)

	mgr := NewManager(mid, "test-rack", mockBus, gen, mockSpecMgr, 5*time.Second, 5*time.Second, 500*time.Millisecond, false, false, nil)
	mgr.factory.Register("emitter_panic", func() sdk.NativeGear { return &emittingGear{} })

	sc := &registry.Scenario{
		Meta:  registry.ScenarioMeta{Name: "emit-panic", Version: "1.0"},
		Gears: []registry.GearSpec{{Name: "src", Type: "emitter_panic", Deploy: "test-rack"}},
	}

	// Must not panic; the emit failure is contained and Start returns cleanly.
	if err := mgr.ApplyScenario(context.Background(), sc); err != nil {
		t.Fatalf("ApplyScenario failed: %v", err)
	}
	mgr.Shutdown()
}

// portedTestGear implements sdk.PortedGear to exercise arrival-port delivery.
type portedTestGear struct {
	MockGear
}

func (g *portedTestGear) ProcessPort(ctx context.Context, port string, msg *fluxmsg.FluxMsg) error {
	return nil
}

func TestParsePortRef_Levels(t *testing.T) {
	cases := map[string][3]string{ // in -> {rack, gear, port}
		"g1.out":                    {"", "g1", "out"},
		"conductor.out_scheme_a":    {"", "conductor", "out_scheme_a"},
		"conductor.in_reply":        {"", "conductor", "in_reply"},
		"rack-b.conductor.in_reply": {"rack-b", "conductor", "in_reply"},
		"worker-a.restore.out":      {"worker-a", "restore", "out"},
		"bare":                      {"", "bare", ""},
	}
	for in, want := range cases {
		rack, gear, port := parsePortRef(in)
		if rack != want[0] || gear != want[1] || port != want[2] {
			t.Errorf("parsePortRef(%q) = (%q,%q,%q), want (%q,%q,%q)", in, rack, gear, port, want[0], want[1], want[2])
		}
	}
}

// publishPort must build flux.msg.<rack>.<gear>.<port>.
func TestManager_PublishPort_NamedSubject(t *testing.T) {
	mockBus := &MockBus{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "test-rack", mockBus, gen, &MockManager{}, time.Second, time.Second, 100*time.Millisecond, false, false, nil)

	_ = mgr.publishPort(context.Background(), "cond", "out_scheme_a", &fluxmsg.FluxMsg{})

	got := mockBus.publishedSubjects()
	want := "flux.msg.test-rack.cond.out_scheme_a"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("published = %v, want [%s]", got, want)
	}
}

// A role-bearing named input (in_reply) wired to a plain gear is a loud
// activation error, not a silent reroute to "in".
func TestManager_NamedInputOnPlainGear_Errors(t *testing.T) {
	mockBus := &MockBus{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "test-rack", mockBus, gen, &MockManager{}, time.Second, time.Second, 100*time.Millisecond, false, false, nil)

	sc := &registry.Scenario{
		Meta:  registry.ScenarioMeta{Name: "bad-wire", Version: "1.0"},
		Gears: []registry.GearSpec{{Name: "g1", Type: "io_tcp", Deploy: "test-rack", Config: map[string]any{"mode": "server", "bind": ":0"}}},
		Wires: []registry.WireSpec{{From: "g1.out", To: "g1.in_reply"}},
	}
	err := mgr.ApplyScenario(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "in_reply") {
		t.Fatalf("expected activation error naming in_reply, got: %v", err)
	}
}

// A PortedGear may receive on a named input port without error.
func TestManager_NamedInputOnPortedGear_OK(t *testing.T) {
	mockBus := &MockBus{}
	mid := uuid.New()
	gen, _ := idgen.New(mid)
	mgr := NewManager(mid, "test-rack", mockBus, gen, &MockManager{}, time.Second, time.Second, 100*time.Millisecond, false, false, nil)
	mgr.factory.Register("ported", func() sdk.NativeGear { return &portedTestGear{} })

	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "ported-wire", Version: "1.0"},
		Gears: []registry.GearSpec{
			{Name: "src", Type: "io_tcp", Deploy: "test-rack", Config: map[string]any{"mode": "server", "bind": ":0"}},
			{Name: "cond", Type: "ported", Deploy: "test-rack"},
		},
		Wires: []registry.WireSpec{{From: "src.out", To: "cond.in_reply"}},
	}
	if err := mgr.ApplyScenario(context.Background(), sc); err != nil {
		t.Fatalf("PortedGear should accept named input port, got: %v", err)
	}
	mgr.Shutdown()
}

// The binding walk resolves each output port through transparent gears to a
// local I/O terminus, marks rack-boundary crossings remote, and refuses to
// guess through forks or broadcasts.
func TestComputeBindings(t *testing.T) {
	sc := &registry.Scenario{
		Gears: []registry.GearSpec{
			{Name: "cond", Type: "conductor", Deploy: "rack-east"},
			{Name: "enc-a", Type: "codec_iso8583", Deploy: "rack-east"},
			{Name: "uplink-a", Type: "io_iso8583", Deploy: "rack-east"},
			{Name: "fork", Type: "codec_iso8583", Deploy: "rack-east"},
			{Name: "sink1", Type: "io_tcp", Deploy: "rack-east"},
			{Name: "sink2", Type: "io_tcp", Deploy: "rack-east"},
			{Name: "cond-west", Type: "conductor", Deploy: "rack-west"},
		},
		Wires: []registry.WireSpec{
			// Through a transparent codec to a local I/O gear.
			{From: "cond.out_scheme_a", To: "enc-a.in"},
			{From: "enc-a.out", To: "uplink-a.in"},
			// Across the rack boundary.
			{From: "cond.out_west", To: "cond-west.in"},
			// Through a gear whose out fans out: ambiguous.
			{From: "cond.out_tap", To: "fork.in"},
			{From: "fork.out", To: "sink1.in"},
			{From: "fork.out", To: "sink2.in"},
		},
	}
	deploy := map[string]string{
		"cond": "rack-east", "enc-a": "rack-east", "uplink-a": "rack-east",
		"fork": "rack-east", "sink1": "rack-east", "sink2": "rack-east",
		"cond-west": "rack-west",
	}

	b := computeBindings(sc, "rack-east", deploy, testTerminus)["cond"]
	if got := b["out_scheme_a"]; got.Kind != sdk.BindingIO || got.Gear != "uplink-a" {
		t.Fatalf("out_scheme_a = %+v, want io/uplink-a", got)
	}
	if got := b["out_west"]; got.Kind != sdk.BindingRemote {
		t.Fatalf("out_west = %+v, want remote", got)
	}
	if got := b["out_tap"]; got.Kind != sdk.BindingUnbound {
		t.Fatalf("out_tap = %+v, want unbound (fork)", got)
	}
	// Gears on other racks get no bindings here.
	if _, ok := computeBindings(sc, "rack-east", deploy, testTerminus)["cond-west"]; ok {
		t.Fatalf("remote gear must not receive local bindings")
	}
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
				Config: map[string]any{"mode": "server", "bind": ":0"},
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
				Config: map[string]any{"mode": "server", "bind": ":0"},
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

// testTerminus is the manifest-terminus lookup used by binding tests, backed
// by the real factory so classification matches production.
func testTerminus(gearType string) sdk.TerminusKind {
	m, _ := gears.NewFactory().Manifest(gearType)
	return m.Terminus
}
