package telemetry

import (
	"context"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

func (ib *InstrumentedBus) Publish(subject string, msg *fluxmsg.FluxMsg) error {
	start := time.Now()

	// Record size approximation (best effort)
	// We don't have serialized size here easily unless we marshal.
	// But NATS bus marshals internally.
	// We'll count '1' message for now.

	err := ib.next.Publish(subject, msg)
	duration := time.Since(start).Milliseconds()

	// Fetch metrics dynamically to use current identity
	m := GetMetrics()
	if m == nil {
		return err
	}

	ctx := context.Background()
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

func (ib *InstrumentedBus) PublishRaw(subject string, data []byte, fluxID uint64) error {
	start := time.Now()

	err := ib.next.PublishRaw(subject, data, fluxID)
	duration := time.Since(start).Milliseconds()

	// Fetch metrics dynamically to use current identity
	m := GetMetrics()
	if m == nil {
		return err
	}

	ctx := context.Background()
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

func (ib *InstrumentedBus) PublishWithContext(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	start := time.Now()

	err := ib.next.PublishWithContext(ctx, subject, msg)
	duration := time.Since(start).Milliseconds()

	// Fetch metrics dynamically
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
	// Wrap handler to measure RX metrics?
	// The handler runs in NATS callback goroutine.
	wrappedHandler := func(msg *fluxmsg.FluxMsg) {
		m := GetMetrics()
		if m != nil {
			ctx := context.Background()
			attrs := metric.WithAttributes(attribute.String("subject", subject))
			m.NatsMessagesReceived.Add(ctx, 1, attrs)
		}

		// Call original
		handler(msg)
	}
	return ib.next.Subscribe(subject, wrappedHandler)
}

func (ib *InstrumentedBus) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	wrappedHandler := func(msg *fluxmsg.FluxMsg) {
		m := GetMetrics()
		if m != nil {
			ctx := context.Background()
			attrs := metric.WithAttributes(attribute.String("subject", subject), attribute.String("durable", durableName))
			m.NatsMessagesReceived.Add(ctx, 1, attrs)
		}

		handler(msg)
	}
	return ib.next.SubscribeDurable(subject, durableName, wrappedHandler)
}
