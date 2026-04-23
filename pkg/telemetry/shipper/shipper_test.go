// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package shipper

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
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
func (m *mockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uint64) error {
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
