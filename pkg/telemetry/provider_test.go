// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"
)

// TelemetryMockHandler for testing
type TelemetryMockHandler struct {
	handled bool
}

func (m *TelemetryMockHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }
func (m *TelemetryMockHandler) Handle(ctx context.Context, r slog.Record) error {
	m.handled = true
	return nil
}
func (m *TelemetryMockHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return m }
func (m *TelemetryMockHandler) WithGroup(name string) slog.Handler       { return m }

func TestMultiHandler(t *testing.T) {
	h1 := &TelemetryMockHandler{}
	h2 := &TelemetryMockHandler{}
	multi := NewMultiHandler(h1, h2)

	if !multi.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Expected MultiHandler to be enabled")
	}

	_ = multi.Handle(context.Background(), slog.Record{Level: slog.LevelInfo})

	if !h1.handled || !h2.handled {
		t.Error("Expected both handlers to be called")
	}

	if len(multi.Handlers()) != 2 {
		t.Errorf("Expected 2 handlers, got %d", len(multi.Handlers()))
	}
}

func TestSourceHandler(t *testing.T) {
	mock := &TelemetryMockHandler{}
	sh := &SourceHandler{next: mock}

	if !sh.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Expected SourceHandler to be enabled")
	}

	// We can't easily verify the AddAttrs logic without a more complex mock,
	// but we can verify the delegation.
	_ = sh.Handle(context.Background(), slog.Record{Level: slog.LevelInfo})
	if !mock.handled {
		t.Error("Expected underlying handler to be called")
	}

	if sh.Next() != mock {
		t.Error("Next() should return the underlying handler")
	}
}

func TestNewMetrics(t *testing.T) {
	mp := metric.NewMeterProvider()
	meter := mp.Meter("test")

	ms, err := NewMetrics(meter)
	if err != nil {
		t.Fatalf("Failed to create metrics: %v", err)
	}

	if ms.BusPublishCount == nil {
		t.Error("Expected BusPublishCount to be initialized")
	}

	// Record a metric
	ms.BusPublishCount.Add(context.Background(), 1)
}

func TestGetMetrics(t *testing.T) {
	// Initially nil
	currentMetrics = nil
	if GetMetrics() != nil {
		t.Error("Expected metrics to be nil initially")
	}

	ms := &Metrics{}
	currentMetrics = ms
	if GetMetrics() != ms {
		t.Error("GetMetrics returned wrong instance")
	}
}
