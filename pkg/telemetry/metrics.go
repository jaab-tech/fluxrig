package telemetry

import (
	"fmt"

	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the initialized OpenTelemetry instruments for FluxRig.
type Metrics struct {
	// Wire Metrics
	HeartbeatsSent       metric.Int64Counter
	WireMessagesSent     metric.Int64Counter
	WireMessagesReceived metric.Int64Counter
	WireBytesIn          metric.Int64Counter
	WireBytesOut         metric.Int64Counter
	WireMessagesDuration metric.Float64Histogram

	// Gear Metrics
	GearProcessingTime metric.Float64Histogram
	GearMessagesIn     metric.Int64Counter
	GearMessagesOut    metric.Int64Counter
	GearErrors         metric.Int64Counter

	// Port Metrics
	PortMessagesIn  metric.Int64Counter
	PortMessagesOut metric.Int64Counter
	PortBytesIn     metric.Int64Counter
	PortBytesOut    metric.Int64Counter

	// NATS Channel Metrics (per subject/stream)
	NatsMessagesPublished metric.Int64Counter // attrs: subject
	NatsMessagesReceived  metric.Int64Counter // attrs: subject
	NatsBytesPublished    metric.Int64Counter // attrs: subject
	NatsBytesReceived     metric.Int64Counter // attrs: subject
	NatsPublishLatency    metric.Float64Histogram
	NatsPublishErrors     metric.Int64Counter // attrs: subject

	// Bus Metrics (aggregate)
	BusPublishCount  metric.Int64Counter
	BusPublishErrors metric.Int64Counter
}

// NewMetrics initializes all metrics using the provided meter.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	m := &Metrics{}
	var err error

	// 1. Rack Metrics
	if m.HeartbeatsSent, err = meter.Int64Counter("heartbeats_sent", metric.WithDescription("Number of heartbeats sent")); err != nil {
		return nil, fmt.Errorf("failed to create heartbeats_sent: %w", err)
	}

	// 2. Wire Metrics
	if m.WireMessagesSent, err = meter.Int64Counter("fluxrig.wire.messages_sent", metric.WithDescription("Total messages sent over wires")); err != nil {
		return nil, fmt.Errorf("failed to create wire.messages_sent: %w", err)
	}
	if m.WireMessagesReceived, err = meter.Int64Counter("fluxrig.wire.messages_received", metric.WithDescription("Total messages received by wires")); err != nil {
		return nil, fmt.Errorf("failed to create wire.messages_received: %w", err)
	}
	if m.WireBytesIn, err = meter.Int64Counter("fluxrig.wire.bytes_in", metric.WithDescription("Total bytes received by wires")); err != nil {
		return nil, fmt.Errorf("failed to create wire.bytes_in: %w", err)
	}
	if m.WireBytesOut, err = meter.Int64Counter("fluxrig.wire.bytes_out", metric.WithDescription("Total bytes sent by wires")); err != nil {
		return nil, fmt.Errorf("failed to create wire.bytes_out: %w", err)
	}
	if m.WireMessagesDuration, err = meter.Float64Histogram("fluxrig.wire.duration_ms", metric.WithDescription("Wire message processing latency in milliseconds"), metric.WithUnit("ms")); err != nil {
		return nil, fmt.Errorf("failed to create wire.duration_ms: %w", err)
	}

	// 2. Gear Metrics
	if m.GearProcessingTime, err = meter.Float64Histogram("fluxrig.gear.processing_time_ms", metric.WithDescription("Gear processing latency in milliseconds"), metric.WithUnit("ms")); err != nil {
		return nil, fmt.Errorf("failed to create gear.processing_time_ms: %w", err)
	}
	if m.GearMessagesIn, err = meter.Int64Counter("fluxrig.gear.messages_in", metric.WithDescription("Messages entering gears")); err != nil {
		return nil, fmt.Errorf("failed to create gear.messages_in: %w", err)
	}
	if m.GearMessagesOut, err = meter.Int64Counter("fluxrig.gear.messages_out", metric.WithDescription("Messages leaving gears")); err != nil {
		return nil, fmt.Errorf("failed to create gear.messages_out: %w", err)
	}
	if m.GearErrors, err = meter.Int64Counter("fluxrig.gear.errors", metric.WithDescription("Errors encountered by gears")); err != nil {
		return nil, fmt.Errorf("failed to create gear.errors: %w", err)
	}

	// 3. Port Metrics
	if m.PortMessagesIn, err = meter.Int64Counter("fluxrig.port.messages_in", metric.WithDescription("Messages entering ports")); err != nil {
		return nil, fmt.Errorf("failed to create port.messages_in: %w", err)
	}
	if m.PortMessagesOut, err = meter.Int64Counter("fluxrig.port.messages_out", metric.WithDescription("Messages leaving ports")); err != nil {
		return nil, fmt.Errorf("failed to create port.messages_out: %w", err)
	}
	if m.PortBytesIn, err = meter.Int64Counter("fluxrig.port.bytes_in", metric.WithDescription("Bytes entering ports")); err != nil {
		return nil, fmt.Errorf("failed to create port.bytes_in: %w", err)
	}
	if m.PortBytesOut, err = meter.Int64Counter("fluxrig.port.bytes_out", metric.WithDescription("Bytes leaving ports")); err != nil {
		return nil, fmt.Errorf("failed to create port.bytes_out: %w", err)
	}

	// 4. NATS Channel Metrics
	if m.NatsMessagesPublished, err = meter.Int64Counter("fluxrig.nats.messages_published", metric.WithDescription("Messages published to NATS subjects")); err != nil {
		return nil, fmt.Errorf("failed to create nats.messages_published: %w", err)
	}
	if m.NatsMessagesReceived, err = meter.Int64Counter("fluxrig.nats.messages_received", metric.WithDescription("Messages received from NATS subjects")); err != nil {
		return nil, fmt.Errorf("failed to create nats.messages_received: %w", err)
	}
	if m.NatsBytesPublished, err = meter.Int64Counter("fluxrig.nats.bytes_published", metric.WithDescription("Bytes published to NATS subjects")); err != nil {
		return nil, fmt.Errorf("failed to create nats.bytes_published: %w", err)
	}
	if m.NatsBytesReceived, err = meter.Int64Counter("fluxrig.nats.bytes_received", metric.WithDescription("Bytes received from NATS subjects")); err != nil {
		return nil, fmt.Errorf("failed to create nats.bytes_received: %w", err)
	}
	if m.NatsPublishLatency, err = meter.Float64Histogram("fluxrig.nats.publish_latency_ms", metric.WithDescription("NATS publish latency in milliseconds"), metric.WithUnit("ms")); err != nil {
		return nil, fmt.Errorf("failed to create nats.publish_latency_ms: %w", err)
	}
	if m.NatsPublishErrors, err = meter.Int64Counter("fluxrig.nats.publish_errors", metric.WithDescription("NATS publish errors")); err != nil {
		return nil, fmt.Errorf("failed to create nats.publish_errors: %w", err)
	}

	// 5. Bus Metrics (Aggregate)
	if m.BusPublishCount, err = meter.Int64Counter("fluxrig.bus.publish_count", metric.WithDescription("Total messages published to bus")); err != nil {
		return nil, fmt.Errorf("failed to create bus.publish_count: %w", err)
	}
	if m.BusPublishErrors, err = meter.Int64Counter("fluxrig.bus.publish_errors", metric.WithDescription("Total bus publish errors")); err != nil {
		return nil, fmt.Errorf("failed to create bus.publish_errors: %w", err)
	}

	return m, nil
}
