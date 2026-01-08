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

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
)

// DualIDSpanProcessor is a rigorous SpanProcessor that ensures every span
// is enriched with the Business Identity (FluxID) if available in the context.
type DualIDSpanProcessor struct{}

func NewDualIDSpanProcessor() *DualIDSpanProcessor {
	return &DualIDSpanProcessor{}
}

// OnStart is called when a span is started.
// Extract FluxID from parent context and add as attribute
func (p *DualIDSpanProcessor) OnStart(ctx context.Context, s trace.ReadWriteSpan) {
	// 1. Extract from Baggage (propagated from upstream or set locally)
	fluxID := FluxIDFromContext(ctx)

	// 2. If present, set as attribute
	if fluxID != "" {
		s.SetAttributes(attribute.String("flux.id", fluxID))
	}
}

// OnEnd is a no-op for this processor.
func (p *DualIDSpanProcessor) OnEnd(s trace.ReadOnlySpan) {}

// Shutdown is a no-op.
func (p *DualIDSpanProcessor) Shutdown(ctx context.Context) error {
	return nil
}

// ForceFlush is a no-op.
func (p *DualIDSpanProcessor) ForceFlush(ctx context.Context) error {
	return nil
}
