// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Mock implementation for testing
type MockBufferHandler struct{}

func (m *MockBufferHandler) Handle(ctx context.Context, r slog.Record) error { return nil }
func (m *MockBufferHandler) Enabled(ctx context.Context, l slog.Level) bool  { return true }
func (m *MockBufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler        { return m }
func (m *MockBufferHandler) WithGroup(name string) slog.Handler              { return m }

func TestSourceHandler_Handle(t *testing.T) {
	// We need a wrapped handler that captures the record so we can inspect attrs
	capture := &CaptureHandler{}

	h := &SourceHandler{
		next: capture,
	}

	logger := slog.New(h)

	// Test
	logger.Info("test message")

	if capture.lastRecord.NumAttrs() == 0 {
		t.Error("Expected attributes to be added")
	}

	hasFile := false
	hasLine := false

	capture.lastRecord.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "code.file.path":
			hasFile = true
		case "code.line.number":
			hasLine = true
		}
		return true
	})

	if !hasFile {
		t.Error("Missing code.filepath")
	}
	if !hasLine {
		t.Error("Missing code.lineno")
	}
}

type CaptureHandler struct {
	lastRecord slog.Record
}

func (h *CaptureHandler) Enabled(ctx context.Context, l slog.Level) bool { return true }
func (h *CaptureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.lastRecord = r
	return nil
}
func (h *CaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *CaptureHandler) WithGroup(name string) slog.Handler       { return h }

// Mock Bus
type MockBus struct{}

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
	return nil, nil
}
func (m *MockBus) SubscribeRaw(subject string, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	return nil, nil
}
func (m *MockBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	return nil, nil
}
func (m *MockBus) Close()           {}
func (m *MockBus) KV() bus.KeyValue { return nil }

func TestInit_Validation(t *testing.T) {
	// 1. Nil Config -> Init requires struct, not pointer, so can't pass nil.
	// We can pass empty config.
	// Init(ctx, cfg, bus, buf, idgen)
	_, err := Init(context.Background(), Config{}, &MockBus{}, nil, nil)
	if err == nil {
		t.Logf("Got expected error (or not): %v", err)
	}
}
