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
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// StartSpan starts a new span linked to the current context.
// Ideally, ctx already contains a parent span.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	tracer := otel.Tracer(instrumentationName)
	return tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

// Log is a convenience wrapper around slog to ensure OTel context is passed.
// It is recommended to use the global slog.InfoContext(ctx, ...) directly,
// since the global logger is configured to use the OTel bridge interactively.
func Log(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	slog.LogAttrs(ctx, level, msg, attrs...)
}

// SetFluxID attaches the business ID to the context for propagation.
// This uses OTel Baggage so it propagates across service boundaries.
func ContextWithFluxID(ctx context.Context, fluxID string) context.Context {
	m, _ := baggage.NewMember("flux.id", fluxID)
	b, _ := baggage.New(m) // Create new baggage with this member
	// Merge with existing baggage if needed
	return baggage.ContextWithBaggage(ctx, b)
}

// FluxIDFromContext retrieves the FluxID from the context's Baggage.
func FluxIDFromContext(ctx context.Context) string {
	b := baggage.FromContext(ctx)
	m := b.Member("flux.id")
	return m.Value()
}
