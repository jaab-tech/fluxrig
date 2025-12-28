package telemetry_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricExporter_Export(t *testing.T) {
	mockBus := bus.NewMockBus()
	writer := telemetry.NewNatsWriter(mockBus, 12345, "test-machine", "flux.telemetry")
	exporter := telemetry.NewMetricExporter(writer)

	// Helper to create dummy metrics
	metrics := &metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{
			{
				Metrics: []metricdata.Metrics{
					{
						Name: "test-metric",
						Data: metricdata.Sum[int64]{
							DataPoints: []metricdata.DataPoint[int64]{
								{Value: 100},
							},
						},
					},
				},
			},
		},
	}

	err := exporter.Export(context.Background(), metrics)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	msgs := mockBus.GetMessages("flux.telemetry.metrics")
	if len(msgs) == 0 {
		t.Fatal("Expected message on 'flux.telemetry.metrics', got none")
	}

	data := msgs[0].Data
	if data == nil {
		// If data allows nil, check impl. But we expect a map
		t.Fatal("Message data is nil")
	}
}
