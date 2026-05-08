// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// InstrumentedBus wraps a bus.Bus to capture telemetry metrics.
type InstrumentedBus struct {
	next bus.Bus
}

// NewInstrumentedBus creates a new wrapper.
// Note: Metrics are fetched dynamically via GetMetrics() to support identity updates.
func NewInstrumentedBus(next bus.Bus, _ *Metrics) bus.Bus {
	return &InstrumentedBus{
		next: next,
	}
}

func (ib *InstrumentedBus) Connect(url string, opts bus.ConnectOptions) error {
	return ib.next.Connect(url, opts)
}

func (ib *InstrumentedBus) Close() {
	ib.next.Close()
}

func (ib *InstrumentedBus) Core() any {
	return ib.next.Core()
}

func (ib *InstrumentedBus) KV() bus.KeyValue {
	return ib.next.KV()
}

func (ib *InstrumentedBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	start := time.Now()

	// 1. Inject Tracing Context
	// If the context contains a Span, we inject it into the message Metadata (W3C TraceContext)
	// and also set the explicit FluxMsg fields for convenience.
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		// Field Mapping (Explicit)
		msg.TraceID = span.SpanContext().TraceID().String()
		// Try to extract FluxID from context for RefFluxID correlation
		if fid := FluxIDFromContext(ctx); fid != "" {
			if ufid, err := uuid.Parse(fid); err == nil {
				msg.RefFluxID = ufid
			}
		}

		// W3C Header Injection (Interoperability)
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}
		otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(msg.Metadata))
	}

	// 2. Publish
	err := ib.next.Publish(ctx, subject, msg)
	duration := time.Since(start).Milliseconds()

	// Fetch metrics dynamically to use current identity
	m := GetMetrics()
	if m == nil {
		return err
	}

	// Use the context for recording if possible, but metrics usually use Background or independent context
	attrs := metric.WithAttributes(attribute.String("subject", subject))

	m.BusPublishCount.Add(ctx, 1)
	m.NatsMessagesPublished.Add(ctx, 1, attrs)
	m.NatsPublishLatency.Record(ctx, float64(duration), attrs)

	if err != nil {
		m.BusPublishErrors.Add(ctx, 1)
		m.NatsPublishErrors.Add(ctx, 1, attrs)
	}

	return err
}

func (ib *InstrumentedBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	start := time.Now()

	err := ib.next.PublishRaw(ctx, subject, data, fluxID)
	duration := time.Since(start).Milliseconds()

	// Fetch metrics dynamically to use current identity
	m := GetMetrics()
	if m == nil {
		return err
	}

	attrs := metric.WithAttributes(attribute.String("subject", subject))

	m.BusPublishCount.Add(ctx, 1)
	m.NatsMessagesPublished.Add(ctx, 1, attrs)
	m.NatsPublishLatency.Record(ctx, float64(duration), attrs)

	if err != nil {
		m.BusPublishErrors.Add(ctx, 1)
		m.NatsPublishErrors.Add(ctx, 1, attrs)
	}

	return err
}

func (ib *InstrumentedBus) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	// Wrap handler to measure RX metrics & Extract Traces
	wrappedHandler := func(ctx context.Context, msg *fluxmsg.FluxMsg) {
		// 1. Extract Tracing Context
		// NATS doesn't pass context over wire natively as header in our wrapper (cbor setup),
		// so we rely on msg.Metadata carrying the W3C traceparent.
		carrier := propagation.MapCarrier(msg.Metadata)
		extractedCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)

		// 2. Start a Span for the handling of this message
		// This links the consumer to the producer in the distributed trace.
		// We use a new context derived from the extracted one.
		tracer := otel.GetTracerProvider().Tracer("fluxrig/bus")
		spanName := "handle_msg " + subject
		handlerCtx, span := tracer.Start(extractedCtx, spanName,
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "nats"),
				attribute.String("messaging.destination", subject),
				attribute.String("messaging.trace_id", msg.TraceID),
				attribute.String("flux.id", msg.FluxID.String()),
			),
		)
		defer span.End()

		m := GetMetrics()
		if m != nil {
			attrs := metric.WithAttributes(attribute.String("subject", subject))
			m.NatsMessagesReceived.Add(handlerCtx, 1, attrs)
		}

		// Call original handler with the TRACED context
		handler(handlerCtx, msg)
	}
	return ib.next.Subscribe(subject, wrappedHandler)
}

func (ib *InstrumentedBus) SubscribeRaw(subject string, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	// For Raw, we just delegate for now (tracing requires unmarshal)
	// TODO: Add metrics for raw subscription
	return ib.next.SubscribeRaw(subject, streamName, handler)
}

func (ib *InstrumentedBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	wrappedHandler := func(ctx context.Context, msg *fluxmsg.FluxMsg) {
		// 1. Extract Tracing Context
		carrier := propagation.MapCarrier(msg.Metadata)
		extractedCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)

		// 2. Start Span
		tracer := otel.GetTracerProvider().Tracer("fluxrig/bus")
		spanName := "handle_msg " + subject
		handlerCtx, span := tracer.Start(extractedCtx, spanName,
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "nats"),
				attribute.String("messaging.destination", subject),
				attribute.String("messaging.durable", durableName),
			),
		)
		defer span.End()

		m := GetMetrics()
		if m != nil {
			attrs := metric.WithAttributes(attribute.String("subject", subject), attribute.String("durable", durableName))
			m.NatsMessagesReceived.Add(handlerCtx, 1, attrs)
		}

		handler(handlerCtx, msg)
	}
	return ib.next.SubscribeDurable(subject, durableName, wrappedHandler)
}
