package telemetry

import (
	"context"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// WatermillMiddleware provides metrics for Watermill handlers.
// It tracks messages processed effectively "entering" the application logic via the Router.
type WatermillMiddleware struct {
	metrics *Metrics
}

func NewWatermillMiddleware(m *Metrics) *WatermillMiddleware {
	return &WatermillMiddleware{metrics: m}
}

func (m *WatermillMiddleware) Middleware(h message.HandlerFunc) message.HandlerFunc {
	return func(msg *message.Message) ([]*message.Message, error) {

		// Attributes
		// Note: Watermill msg.Metadata can contain trace context, etc.
		// We can extract topic/handler info if available in context or msg?
		// Watermill router usually puts handler name in context?
		// For now simple attributes.

		// Execute handler
		start := time.Now()
		msgs, err := h(msg)
		duration := float64(time.Since(start).Microseconds()) / 1000.0 // Float ms for precision

		ctx := context.Background() // Or better, use msg.Context() if it carries baggage?
		// Ideally we use the context from the message if propagated.
		if msg.Context() != nil {
			ctx = msg.Context()
		}

		// What metrics to use?
		// We can map this to "Port" or "App" metrics.
		// Since this is the Mixer Router, let's genericize or reuse "Wire" if appropriate
		// or just use generic "router_handler" metrics if we had them.
		// For now, let's use WireMessagesReceived since it's "Received off the wire"

		attrs := metric.WithAttributes(
			attribute.String("component", "router"),
			// attribute.String("handler", ...?) // Handler name hard to get here genericly without extra setup
		)

		if m.metrics != nil {
			m.metrics.WireMessagesReceived.Add(ctx, 1, attrs)
			m.metrics.WireBytesIn.Add(ctx, int64(len(msg.Payload)), attrs)
			m.metrics.WireMessagesDuration.Record(ctx, duration, attrs)
		}

		return msgs, err
	}
}
