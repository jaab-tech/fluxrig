// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// fake transport routes a request in-process to a handler, standing in for
// NATS request/reply. Setting down=true makes it fail, as no holder answering
// would.
type fakeTransport struct {
	mu     sync.Mutex
	handle func(subject string, data []byte) []byte
	down   bool
}

func (f *fakeTransport) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	f.mu.Lock()
	down, h := f.down, f.handle
	f.mu.Unlock()
	if down || h == nil {
		return nil, errors.New("no responders")
	}
	return h(subject, data), nil
}

// memRegistry is a minimal registry.Registry that records calls, so the test
// can assert whether a write ran locally or was forwarded.
type memRegistry struct {
	mu        sync.Mutex
	registers int
	approves  int
	approveBy map[uuid.UUID]string
	failWith  error
}

func (m *memRegistry) RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name, secret, ip string, port int, version string, config, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWith != nil {
		return nil, m.failWith
	}
	m.registers++
	// A fresh registration returns the secret it issued, as the store does.
	return &registry.Rack{MachineID: machineID, Name: name, Status: "pending", Secret: "issued-" + name}, nil
}
func (m *memRegistry) Approve(ctx context.Context, id uuid.UUID, newName string) (*registry.Rack, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWith != nil {
		return nil, m.failWith
	}
	m.approves++
	if m.approveBy == nil {
		m.approveBy = map[uuid.UUID]string{}
	}
	m.approveBy[id] = newName
	return &registry.Rack{MachineID: id, Name: newName, Status: "active"}, nil
}
func (m *memRegistry) Register(ctx context.Context, machineID uuid.UUID, name, secret, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	return m.RegisterEntity(ctx, 4, machineID, name, secret, ip, port, version, config, nil, mixerID)
}
func (m *memRegistry) RemoveEntity(ctx context.Context, typeID uint8, id uuid.UUID) error {
	return m.failWith
}
func (m *memRegistry) Remove(ctx context.Context, id uuid.UUID) error      { return nil }
func (m *memRegistry) RemoveByName(ctx context.Context, name string) error { return m.failWith }
func (m *memRegistry) UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error {
	return m.failWith
}
func (m *memRegistry) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return nil
}
func (m *memRegistry) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return nil, nil
}
func (m *memRegistry) Get(ctx context.Context, id uuid.UUID) (*registry.Rack, error) { return nil, nil }
func (m *memRegistry) Heartbeat(ctx context.Context, id uuid.UUID, stats, config map[string]any) error {
	return nil
}
func (m *memRegistry) HeartbeatEntity(ctx context.Context, typeID uint8, id uuid.UUID, stats, config map[string]any) error {
	return nil
}
func (m *memRegistry) SetAutoAdopt(bool)         {}
func (m *memRegistry) SetBootstrapSecret(string) {}
func (m *memRegistry) QueryLogs(ctx context.Context, q registry.LogQuery) ([]registry.LogEntry, error) {
	return nil, nil
}
func (m *memRegistry) QueryMetrics(ctx context.Context, q registry.MetricQuery) ([]registry.MetricEntry, error) {
	return nil, nil
}
func (m *memRegistry) ClearScenarioEntities(ctx context.Context) error { return nil }

// staticHolder is a Holder whose answer the test sets.
type staticHolder struct{ held bool }

func (h *staticHolder) Held() bool { return h.held }

// wire points a Forwarder at a Server over the fake transport.
func wire(t *testing.T, holder *memRegistry, held *staticHolder) *Registry {
	t.Helper()
	tr := &fakeTransport{}
	srv := NewServer(holder, nil, nil, slog.Default())
	tr.handle = func(subject string, data []byte) []byte {
		req, ok := decodeRequest(t, data)
		if !ok {
			return []byte(`{"error_code":"internal"}`)
		}
		var resp Response
		rack, err := srv.execute(context.Background(), req)
		if err != nil {
			resp = Response{ErrorCode: CodeOf(err), ErrorText: err.Error()}
		} else {
			resp = Response{Rack: rack}
		}
		return encodeResponse(t, resp)
	}
	fwd := NewForwarder(tr, time.Second, slog.Default())
	return NewRegistry(holder, fwd, held)
}

// A non-holder's control write is forwarded to the holder and lands there, not
// locally.
func TestForward_NonHolderForwardsToHolder(t *testing.T) {
	holderReg := &memRegistry{}
	nonHolder := &memRegistry{}
	held := &staticHolder{held: false} // this Mixer does not hold the lease

	r := wire(t, holderReg, held)
	r.local = nonHolder

	id := uuid.New()
	rack, err := r.Approve(context.Background(), id, "rack-renamed")
	require.NoError(t, err)
	assert.Equal(t, "rack-renamed", rack.Name)

	assert.Equal(t, 1, holderReg.approves, "the write ran on the holder")
	assert.Zero(t, nonHolder.approves, "the non-holder did not write locally")
}

// The holder writes locally, never forwarding to itself.
func TestForward_HolderWritesLocally(t *testing.T) {
	local := &memRegistry{}
	held := &staticHolder{held: true}
	r := wire(t, &memRegistry{}, held)
	r.local = local

	_, err := r.Approve(context.Background(), uuid.New(), "x")
	require.NoError(t, err)
	assert.Equal(t, 1, local.approves, "the holder wrote locally")
}

// A typed error crosses the hop: a name conflict on the holder is a name
// conflict at the caller.
func TestForward_ErrorTypeSurvives(t *testing.T) {
	holderReg := &memRegistry{failWith: registry.ErrNameConflict}
	held := &staticHolder{held: false}
	r := wire(t, holderReg, held)

	_, err := r.Approve(context.Background(), uuid.New(), "dup")
	require.ErrorIs(t, err, registry.ErrNameConflict, "the conflict survives the hop")
}

// With no holder answering, the write is refused with ErrForbiddenNoHolder,
// never a hang and never a silent success.
func TestForward_NoHolderIsRefused(t *testing.T) {
	held := &staticHolder{held: false}
	tr := &fakeTransport{down: true}
	fwd := NewForwarder(tr, 200*time.Millisecond, slog.Default())
	r := NewRegistry(&memRegistry{}, fwd, held)

	_, err := r.Approve(context.Background(), uuid.New(), "x")
	require.ErrorIs(t, err, ErrForbiddenNoHolder)
}

// A nil holder is pass-through: a single Mixer is unaffected.
func TestForward_NilHolderIsPassThrough(t *testing.T) {
	local := &memRegistry{}
	r := NewRegistry(local, nil, nil)
	_, err := r.Approve(context.Background(), uuid.New(), "x")
	require.NoError(t, err)
	assert.Equal(t, 1, local.approves)
}

func decodeRequest(t *testing.T, data []byte) (Request, bool) {
	t.Helper()
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, false
	}
	return req, true
}

func encodeResponse(t *testing.T, resp Response) []byte {
	t.Helper()
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
