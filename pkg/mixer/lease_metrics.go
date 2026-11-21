// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Lease event names as the metric reports them.
const (
	leaseEventAcquired = "acquired"
	leaseEventLost     = "lost"
	leaseEventReleased = "released"
)

// leaseMetrics counts the write lease transitions of this Mixer, and reports
// them with whether it holds the lease now. Across a healthy cluster
// flux.mixer.lease.held sums to 1.
type leaseMetrics struct {
	acquired, lost, released atomic.Int64
}

// record counts one event from the lease reporter. "renew_failed" is how the
// lease reports a holder that lost it.
func (m *leaseMetrics) record(event string) {
	switch event {
	case "acquired":
		m.acquired.Add(1)
	case "renew_failed":
		m.lost.Add(1)
	case "released":
		m.released.Add(1)
	}
}

// register adds the lease instruments to meter. held reports whether this
// Mixer holds the lease at collection time.
func (m *leaseMetrics) register(meter metric.Meter, held func() bool) error {
	_, err := meter.Int64ObservableGauge("flux.mixer.lease.held",
		metric.WithDescription("1 when this Mixer holds the write lease, else 0"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			v := int64(0)
			if held() {
				v = 1
			}
			o.Observe(v)
			return nil
		}))
	if err != nil {
		return fmt.Errorf("mixer: lease metrics: held: %w", err)
	}
	_, err = meter.Int64ObservableCounter("flux.mixer.lease.transitions",
		metric.WithDescription("Write lease transitions of this Mixer, by event: acquired, lost, released"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.acquired.Load(), metric.WithAttributes(attribute.String("event", leaseEventAcquired)))
			o.Observe(m.lost.Load(), metric.WithAttributes(attribute.String("event", leaseEventLost)))
			o.Observe(m.released.Load(), metric.WithAttributes(attribute.String("event", leaseEventReleased)))
			return nil
		}))
	if err != nil {
		return fmt.Errorf("mixer: lease metrics: transitions: %w", err)
	}
	return nil
}
