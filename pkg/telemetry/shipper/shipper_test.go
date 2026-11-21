// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package shipper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"
)

type mockBus struct {
	published []*fluxmsg.FluxMsg
}

func (m *mockBus) Connect(url string, opts bus.ConnectOptions) error { return nil }
func (m *mockBus) Close()                                            {}
func (m *mockBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	m.published = append(m.published, msg)
	return nil
}
func (m *mockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	var msg fluxmsg.FluxMsg
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return err
	}
	m.published = append(m.published, &msg)
	return nil
}
func (m *mockBus) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *mockBus) SubscribeRaw(subject string, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	return nil, nil
}
func (m *mockBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *mockBus) KV() bus.KeyValue { return nil }
func (m *mockBus) Core() any        { return nil }

func TestShipper(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := tmpDir + "/cursor.json"

	w, err := wal.Open(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to open WAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write a proper FluxMsg
	msg := fluxmsg.New()
	msg.Data = map[string]any{"foo": "bar"}
	msg.Metadata["type"] = "telemetry.log"

	if errWr := w.Write(msg); errWr != nil {
		t.Fatalf("Write failed: %v", errWr)
	}

	cursor, err := NewCursor(cursorPath)
	if err != nil {
		t.Fatalf("Failed to create cursor: %v", err)
	}
	// Use pointer to mockBus to satisfy interface
	mb := &mockBus{}
	// Shipper
	s := NewLogShipper(mb, w, cursor, "test.subject", 100, 0, 0)
	s.Start()

	time.Sleep(500 * time.Millisecond)
	s.Stop()

	if len(mb.published) != 1 {
		t.Fatalf("Expected 1 published msg, got %d", len(mb.published))
	}

	// Check content
	dataMap := mb.published[0].Data
	if val, ok := dataMap["foo"]; !ok || val != "bar" {
		t.Errorf("Expected foo=bar, got %v", val)
	}

	// Verify Offset
	if cursor.Get() != 1 {
		t.Errorf("Expected cursor offset 1, got %d", cursor.Get())
	}
}

func TestShipperMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := tmpDir + "/metric_cursor.json"

	w, err := wal.Open(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to open WAL: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write a Metric msg
	msg := fluxmsg.New()
	msg.Metadata["type"] = "telemetry.metric"

	if errWr := w.Write(msg); errWr != nil {
		t.Fatalf("Write failed: %v", errWr)
	}

	cursor, _ := NewCursor(cursorPath)
	mb := &mockBus{}
	// NewLogShipper handles both
	s := NewLogShipper(mb, w, cursor, "test.telemetry", 100, 0, 0)
	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	if len(mb.published) != 1 {
		t.Fatalf("Expected 1 published metric, got %d", len(mb.published))
	}
}

func TestCursor(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/test_cursor.json"

	c, err := NewCursor(path)
	if err != nil {
		t.Fatalf("NewCursor failed: %v", err)
	}

	c.Update(100)
	if c.Get() != 100 {
		t.Errorf("Expected 100, got %d", c.Get())
	}

	if err := c.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload
	c2, _ := NewCursor(path)
	if c2.Get() != 100 {
		t.Errorf("Expected 100 after reload, got %d", c2.Get())
	}

	// Invalid path error case
	c3, _ := NewCursor("/invalid/path/cursor.json")
	if err := c3.Save(); err == nil {
		t.Error("Expected error for saving to invalid path")
	}
}

// slowBus costs one round trip per call, as a publish to a stream leader
// across a slow link does, whether it carries one entry or a batch. It
// refuses the batch call at refuseAt, once, after storing the ones before.
type slowBus struct {
	mockBus
	mu       sync.Mutex
	rtt      time.Duration
	calls    int
	refuseAt int
	bodies   []string
}

func (b *slowBus) PublishRaw(ctx context.Context, subject string, data []byte, id uuid.UUID) error {
	_, err := b.PublishRawBatch(ctx, subject, []bus.RawMessage{{Data: data, ID: id}})
	return err
}

func (b *slowBus) store(data []byte) {
	var msg fluxmsg.FluxMsg
	_ = cbor.Unmarshal(data, &msg)
	b.bodies = append(b.bodies, fmt.Sprint(msg.Data["n"]))
}

// batchingBus is a slowBus that takes a batch in one call.
type batchingBus struct{ *slowBus }

func (b *slowBus) PublishRawBatch(_ context.Context, _ string, msgs []bus.RawMessage) (int, error) {
	time.Sleep(b.rtt) // the simulated round trip
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	for i, m := range msgs {
		if b.refuseAt > 0 && len(b.bodies) == b.refuseAt {
			b.refuseAt = 0
			return i, errors.New("the stream did not answer")
		}
		b.store(m.Data)
	}
	return len(msgs), nil
}

// oneByOne hides the batch capability, as a bus without it.
type oneByOne struct{ inner *slowBus }

func (o oneByOne) Connect(string, bus.ConnectOptions) error { return nil }
func (o oneByOne) Close()                                   {}
func (o oneByOne) Publish(ctx context.Context, s string, m *fluxmsg.FluxMsg) error {
	return o.inner.Publish(ctx, s, m)
}
func (o oneByOne) PublishRaw(ctx context.Context, s string, d []byte, id uuid.UUID) error {
	return o.inner.PublishRaw(ctx, s, d, id)
}
func (o oneByOne) Subscribe(s string, h bus.Handler) (bus.Subscription, error) {
	return o.inner.Subscribe(s, h)
}
func (o oneByOne) SubscribeRaw(s, st string, h bus.RawHandler) (bus.Subscription, error) {
	return o.inner.SubscribeRaw(s, st, h)
}
func (o oneByOne) SubscribeDurable(s, d string, h bus.Handler) (bus.Subscription, error) {
	return o.inner.SubscribeDurable(s, d, h)
}
func (o oneByOne) KV() bus.KeyValue { return nil }
func (o oneByOne) Core() any        { return nil }

func writeEntries(t *testing.T, w *wal.WAL, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		msg := fluxmsg.New()
		msg.Data = map[string]any{"n": i}
		msg.Metadata["type"] = "telemetry.log"
		if err := w.Write(msg); err != nil {
			t.Fatal(err)
		}
	}
}

// shipAll runs a shipper over n entries until the cursor reaches n, and
// returns how long it took.
func shipAll(t *testing.T, b bus.Bus, n int) time.Duration {
	t.Helper()
	dir := t.TempDir()
	w, err := wal.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	writeEntries(t, w, n)
	cursor, err := NewCursor(dir + "/cursor.json")
	if err != nil {
		t.Fatal(err)
	}
	s := NewLogShipper(b, w, cursor, "test.subject", 100, 0, 0)
	start := time.Now()
	s.Start()
	defer s.Stop()
	deadline := time.Now().Add(30 * time.Second)
	for cursor.Get() < uint64(n) {
		if time.Now().After(deadline) {
			t.Fatalf("shipped %d of %d", cursor.Get(), n)
		}
		time.Sleep(10 * time.Millisecond) // poll interval
	}
	return time.Since(start)
}

// A Rack whose log grows faster than one round trip per line: shipped one
// line at a time it fell behind for good; in batches it keeps up, in order.
func TestShipper_BatchesKeepUpWithASlowLink(t *testing.T) {
	const n = 100
	single := &slowBus{rtt: 50 * time.Millisecond}
	oneAtATime := shipAll(t, oneByOne{single}, n)
	batched := &slowBus{rtt: 50 * time.Millisecond}
	inBatches := shipAll(t, batchingBus{batched}, n)

	if oneAtATime < n*50*time.Millisecond {
		t.Errorf("one at a time took %s, want at least a round trip per line", oneAtATime)
	}
	if inBatches > oneAtATime/5 {
		t.Errorf("in batches took %s, one at a time %s", inBatches, oneAtATime)
	}
	if len(batched.bodies) != n {
		t.Fatalf("shipped %d lines, want %d", len(batched.bodies), n)
	}
	for i, b := range batched.bodies {
		if b != fmt.Sprint(i+1) {
			t.Fatalf("line %d is %s: the order must be kept", i+1, b)
		}
	}
	t.Logf("%d lines: one at a time %s, in %d calls %s", n, oneAtATime.Round(time.Millisecond), batched.calls, inBatches.Round(time.Millisecond))
}

// A batch the stream takes in part moves the cursor past what it took, and
// the rest goes in the next round: every line once, in order.
func TestShipper_APartialBatchIsResentFromWhereItStopped(t *testing.T) {
	const n = 30
	b := &slowBus{refuseAt: 10}
	shipAll(t, batchingBus{b}, n)
	if len(b.bodies) != n {
		t.Fatalf("shipped %d lines, want %d: %v", len(b.bodies), n, b.bodies)
	}
	for i, body := range b.bodies {
		if body != fmt.Sprint(i+1) {
			t.Fatalf("line %d is %s: every line once, in order", i+1, body)
		}
	}
}
