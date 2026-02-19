// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

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
