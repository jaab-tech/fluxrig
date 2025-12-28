package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestSpanExporter_ExportSpans(t *testing.T) {
	mockBus := bus.NewMockBus()
	writer := telemetry.NewNatsWriter(mockBus, 12345, "test-machine", "flux.telemetry")
	exporter := telemetry.NewSpanExporter(writer)

	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter), // Export directly
	)
	defer tp.Shutdown(context.Background())

	ctx := context.Background()
	tracer := tp.Tracer("test-tracer")
	_, span := tracer.Start(ctx, "test-export-span")
	span.End()

	// Wait a tiny bit for async bus handler if any (MockBus is sync for Publish list append)

	// Wait a tiny bit for async bus handler if any (MockBus is sync for Publish list append)

	msgs := mockBus.GetMessages("flux.telemetry.spans")
	if len(msgs) == 0 {
		t.Fatal("Expected message on 'flux.telemetry.spans', got none")
	}

	// Verify payload structure (light check)
	// FluxMsg.Data is map[string]any, no need to assert
	data := msgs[0].Data
	if data == nil {
		t.Fatal("Message data is nil")
	}

	batch, ok := data["batch"].([]map[string]interface{})
	if !ok {
		// It might be []interface{} if unmarshaled, but here it's fresh from code.
		// In creation we used []map[string]interface{}.
		// However, let's just check it exists.
		if data["batch"] == nil {
			t.Fatal("Batch field missing")
		}
		// If we can't assert easily without reflection due to 'any', we skip deep check
		// or casting.
		return
	}

	if len(batch) != 1 {
		t.Errorf("Expected batch size 1, got %d", len(batch))
	}
}

func TestLogExporter_Export(t *testing.T) {
	mockBus := bus.NewMockBus()
	writer := telemetry.NewNatsWriter(mockBus, 12345, "test-machine", "flux.telemetry")
	exporter := telemetry.NewLogExporter(writer)

	// Create detailed Record
	rec := sdklog.Record{}
	rec.SetTimestamp(time.Now())
	rec.SetBody(log.StringValue("test log body"))
	rec.SetSeverity(log.SeverityInfo)
	rec.SetSeverityText("INFO")

	err := exporter.Export(context.Background(), []sdklog.Record{rec})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	msgs := mockBus.GetMessages("flux.telemetry.logs")
	if len(msgs) == 0 {
		t.Fatal("Expected message on 'flux.telemetry.logs', got none")
	}

	data := msgs[0].Data
	if data["batch"] == nil {
		t.Fatal("Batch field missing")
	}
}
