// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

func TestMetricExporter_Export(t *testing.T) {
	mockBus := bus.NewMockBus()
	gen, _ := idgen.New(uuid.New())
	writer := telemetry.NewNatsWriter(mockBus, uuid.New(), "test-machine", "flux.telemetry", gen, 1) // 1s flush
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

	msgs := mockBus.GetMessages("flux.telemetry.test-machine.metrics")
	if len(msgs) == 0 {
		t.Fatal("Expected message on 'flux.telemetry.test-machine.metrics', got none")
	}

	data := msgs[0].Data
	if data == nil {
		// If data allows nil, check impl. But we expect a map
		t.Fatal("Message data is nil")
	}
}
