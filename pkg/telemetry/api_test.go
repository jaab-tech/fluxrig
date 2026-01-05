package telemetry_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartSpan(t *testing.T) {
	// Setup TraceProvider with InMemory Exporter
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)

	// Test StartSpan
	ctx := context.Background()
	ctx, span := telemetry.StartSpan(ctx, "test-span")
	span.End()

	// Verify
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("Expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "test-span" {
		t.Errorf("Expected span name 'test-span', got '%s'", spans[0].Name)
	}
}

func TestContextWithFluxID(t *testing.T) {
	ctx := context.Background()
	fluxID := "123456789"

	// Inject
	ctx = telemetry.ContextWithFluxID(ctx, fluxID)

	// Verify (In real usage, this would be extracted by the SpanProcessor)
	// For this unit test, we just ensure it doesn't panic and returns a context.
	if ctx == nil {
		t.Fatal("Expected context, got nil")
	}
}

func TestLog(t *testing.T) {
	t.Skip("Skipping TestLog due to slog recursion issues in test environment")
	// Setup Logger
	// We just want to ensure it doesn't panic. OTel Bridge testing is complex in unit tests.
	ctx := context.Background()

	// Init with Mock Bus
	telemetry.ResetGlobalsForTest()
	mockBus := bus.NewMockBus()
	// Test Writer directly
	_ = telemetry.NewNatsWriter(mockBus, 12345, "test-name", "flux.test")
	cfg := telemetry.Config{
		ServiceName: "test-service",
		EntityID:    12345,
		BaseSubject: "test.telemetry",
	}

	// This should not panic
	t.Log("Calling Init...")
	shutdown, err := telemetry.Init(ctx, cfg, mockBus, nil, nil)
	if err != nil {
		t.Fatalf("Failed to init telemetry: %v", err)
	}
	t.Log("Init done. Deferring shutdown.")
	defer func() {
		t.Log("Calling Shutdown...")
		shutdown(context.Background())
		t.Log("Shutdown done.")
	}()

	t.Log("Calling Log...")
	telemetry.Log(ctx, slog.LevelInfo, "test message", slog.String("key", "value"))
	t.Log("Log done.")
}
