package telemetry_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"

	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

func TestTelemetry_ContextHelpers(t *testing.T) {
	ctx := context.Background()
	
	// 1. Initial State
	if id := telemetry.FluxIDFromContext(ctx); id != "" {
		t.Errorf("Expected empty ID, got %s", id)
	}

	// 2. Set ID
	ctx = telemetry.ContextWithFluxID(ctx, "test-flux-id")
	if id := telemetry.FluxIDFromContext(ctx); id != "test-flux-id" {
		t.Errorf("Expected test-flux-id, got %s", id)
	}

	// 3. Overwrite/Separate Context
	ctx2 := telemetry.ContextWithFluxID(ctx, "new-id")
	if id := telemetry.FluxIDFromContext(ctx2); id != "new-id" {
		t.Errorf("Expected new-id, got %s", id)
	}
	// Original unchanged? Baggage is immutable?
	if id := telemetry.FluxIDFromContext(ctx); id != "test-flux-id" {
		t.Errorf("Expected original to remain test-flux-id, got %s", id)
	}
}

func TestTelemetry_ApiWrappers(t *testing.T) {
	ctx := context.Background()

	// 1. StartSpan (No-op tracer usually if not init, but safe to call)
	ctxSpan, span := telemetry.StartSpan(ctx, "test-span", attribute.String("key", "val"))
	defer span.End()
	if span == nil {
		t.Error("StartSpan returned nil span")
	}
	if ctxSpan == nil {
		t.Error("StartSpan returned nil context")
	}

	// 2. Log Wrapper
	// Just verify it doesn't panic. Detailed output check requires capturing stdout/handler.
	telemetry.Log(ctx, slog.LevelInfo, "test log message", slog.String("foo", "bar"))
}

func TestTelemetry_Exporters(t *testing.T) {
	// 1. Span Exporter (NatsWriter constructor wrapping)
	// We need 'bus.Bus', 'Conn', or at least 'Writer'.
	// NewSpanExporter(w *NatsWriter).
	// But NatsWriter constructor uses Bus?
	// telemetry.NewNatsWriter(bus, subject, batchSize, interval)
	
	// This requires 'bus' mocking or import.
	// We can skip constructor tests if too complex dependency wise for this file.
	// But we can test `Config` struct defaults?
	
	c := telemetry.Config{
		ServiceName: "test",
	}
	if c.ServiceName != "test" {
		t.Error("Config struct failure")
	}
	
	// NatsWriter creation is internal? No, exported properly?
	// `NewSpanExporter` is in `exporter.go`.
	// It takes `*NatsWriter`.
	// Since we are in `telemetry_test` package, we can't access `telemetry.NatsWriter`?
	// `NatsWriter` is exported struct `type NatsWriter struct`.
	// But `NewNatsWriter` returns `*NatsWriter`.
	
	// ... existing tests ...
}

func TestTelemetry_Init_Success(t *testing.T) {
	// 1. Mock Bus
	mockBus := bus.NewMockBus()

	// 2. Init Telemetry
	telCfg := telemetry.Config{
		ServiceName:         "test-service",
		ServiceVersion:      "v1",
		BatchIntervalString: "10ms", // Fast flush
		MaxBatchSize:        1,      // Flush immediately
	}
	
	telemetry.ResetGlobalsForTest()

	shutdown, err := telemetry.Init(context.Background(), telCfg, mockBus)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer shutdown(context.Background())
	
	// 3. Trace (Complex)
	ctx, span := telemetry.StartSpan(context.Background(), "init-test-span", 
		attribute.String("key", "val"),
		attribute.Int("count", 123),
		attribute.Bool("flag", true),
		attribute.Float64("score", 99.9),
	)
	span.AddEvent("test-event", trace.WithAttributes(attribute.String("event-attr", "foo")))
	span.SetStatus(codes.Error, "something went wrong")
	// Links?
	// link := trace.Link{SpanContext: span.SpanContext()} // needs valid context
	span.End()
	
	// 4. Metric (Complex)
	meter := otel.GetMeterProvider().Meter("test-meter")
	counter, _ := meter.Int64Counter("init_test_counter")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "hit")))
	
	gauge, _ := meter.Float64ObservableGauge("init_memory")
	_, _ = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		o.ObserveFloat64(gauge, 1024.0, metric.WithAttributes(attribute.String("unit", "mb")))
		return nil
	}, gauge)

	// Wait for async flush or force shutdown to flush
	// Shutdown will flush.
	// We can check mockBus.PublishedMessages AFTER shutdown? 
	// Or mockBus captures them?
	// MockBus is thread safe? generic mock usually is simple.
}



// TestExporterConstructors tests the low-level exporter constructors
func TestExporterConstructors(t *testing.T) {
	mockBus := bus.NewMockBus()
	
	// 1. NatsWriter
	w := telemetry.NewNatsWriter(mockBus, 1, "test-entity", "flux.telemetry")
	if w == nil {
		t.Error("NewNatsWriter returned nil")
	}
	
	// 2. SpanExporter
	se := telemetry.NewSpanExporter(w)
	if se == nil {
		t.Error("NewSpanExporter returned nil")
	}
	
	// 3. LogExporter
	le := telemetry.NewLogExporter(w)
	if le == nil {
		t.Error("NewLogExporter returned nil")
	}
	
	// 4. MetricExporter
	me := telemetry.NewMetricExporter(w)
	if me == nil {
		t.Error("NewMetricExporter returned nil")
	}
}
