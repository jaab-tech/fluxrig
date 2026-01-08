// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricExporter_Export(t *testing.T) {
	mockBus := bus.NewMockBus()
	gen, _ := idgen.New(1)
	writer := telemetry.NewNatsWriter(mockBus, 12345, "test-machine", "flux.telemetry", gen)
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
