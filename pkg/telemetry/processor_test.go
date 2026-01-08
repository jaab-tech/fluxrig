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

	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDualIDSpanProcessor_OnStart(t *testing.T) {
	// 1. Setup
	tp := trace.NewTracerProvider()
	processor := telemetry.NewDualIDSpanProcessor()

	// Create a span recorder to inspect the span
	recorder := tracetest.NewSpanRecorder()

	// 2. Case: With FluxID in Context
	ctx := context.Background()
	fluxID := "test-flux-id-123"
	ctx = telemetry.ContextWithFluxID(ctx, fluxID)

	// Create a span manually to pass ReadWriteSpan interface
	// Hard to mock ReadWriteSpan directly, easier to use the SDK logic or a mock.
	// Since OnStart takes trace.ReadWriteSpan, let's use a real tracer but intercept via a recorder?
	// Actually, easier way:

	tracer := tp.Tracer("test")
	// Use the processor in the provider to test integration
	tp.RegisterSpanProcessor(processor)
	tp.RegisterSpanProcessor(recorder)

	_, span := tracer.Start(ctx, "test-span")
	span.End()

	// 3. Verify
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("Expected 1 span, got %d", len(spans))
	}

	found := false
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "flux.id" && attr.Value.AsString() == fluxID {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected attribute 'flux.id' with value '%s', but not found", fluxID)
	}
}

func TestDualIDSpanProcessor_NoID(t *testing.T) {
	tp := trace.NewTracerProvider()
	processor := telemetry.NewDualIDSpanProcessor()
	recorder := tracetest.NewSpanRecorder()
	tp.RegisterSpanProcessor(processor)
	tp.RegisterSpanProcessor(recorder)

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span-no-id")
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("Expected 1 span")
	}

	for _, attr := range spans[0].Attributes() {
		if attr.Key == "flux.id" {
			t.Error("Did not expect 'flux.id' attribute when context is empty")
		}
	}
}
