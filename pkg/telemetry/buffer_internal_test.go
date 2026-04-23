// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"log/slog"
	"testing"
)

type MockSlogHandler struct {
	records []slog.Record
	enabled bool
}

func (m *MockSlogHandler) Enabled(ctx context.Context, level slog.Level) bool { return m.enabled }
func (m *MockSlogHandler) Handle(ctx context.Context, r slog.Record) error {
	m.records = append(m.records, r)
	return nil
}
func (m *MockSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return m }
func (m *MockSlogHandler) WithGroup(name string) slog.Handler       { return m }

func TestBufferHandler_Lifecycle(t *testing.T) {
	mock := &MockSlogHandler{enabled: true}
	h := NewBufferHandler(mock)

	if h.Next() != mock {
		t.Error("Next() did not return expected handler")
	}

	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled() check failed")
	}

	// 1. Log something
	_ = h.Handle(context.Background(), slog.Record{Level: slog.LevelInfo, Message: "test-1"})

	if len(h.buffer.records) != 1 {
		t.Errorf("Expected 1 record in buffer, got %d", len(h.buffer.records))
	}
	if len(mock.records) != 1 {
		t.Errorf("Expected 1 record in mock, got %d", len(mock.records))
	}

	// 2. With Attrs/Group
	h2 := h.WithAttrs([]slog.Attr{slog.String("foo", "bar")})
	if h2.(*BufferHandler).buffer != h.buffer {
		t.Error("Buffer should be shared between derived handlers")
	}

	h3 := h.WithGroup("test-group")
	if h3.(*BufferHandler).buffer != h.buffer {
		t.Error("Buffer should be shared between derived handlers")
	}

	// 3. Flush
	target := &MockSlogHandler{enabled: true}
	err := h.FlushTo(context.Background(), target)
	if err != nil {
		t.Fatalf("FlushTo failed: %v", err)
	}

	if len(target.records) != 1 {
		t.Errorf("Expected 1 record in target, got %d", len(target.records))
	}
	if len(h.buffer.records) != 0 {
		t.Error("Buffer should be empty after flush")
	}
}
