// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// The Mixer's own metrics take the path of a Rack's: one message per point
// on <base>.<entity>.metrics, carrying the metric name and the entity.
func TestInitMeterProvider_PublishesOnTheTelemetrySubject(t *testing.T) {
	mockBus := bus.NewMockBus()
	id := uuid.New()
	gen, err := idgen.New(id)
	require.NoError(t, err)
	w := telemetry.NewNatsWriter(mockBus, id, "mixer-1", "flux.telemetry", gen, time.Second)
	mp, err := telemetry.InitMeterProvider(telemetry.MeterConfig{
		Writer: w, ServiceName: "fluxrig-mixer", EntityID: id, EntityName: "mixer-1",
		Component: "MIXER", Interval: time.Hour, // exported by the shutdown below
	})
	require.NoError(t, err)

	_, err = mp.Meter("test").Int64ObservableGauge("flux.mixer.lease.held",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1)
			return nil
		}))
	require.NoError(t, err)
	require.NoError(t, telemetry.ShutdownMeterProvider(mp, 5*time.Second))

	msgs := mockBus.GetMessages("flux.telemetry.mixer-1.metrics")
	require.NotEmpty(t, msgs, "the shutdown exports the points")
	found := false
	for _, m := range msgs {
		if m.Metadata["metric.name"] == "flux.mixer.lease.held" {
			found = true
			assert.Equal(t, "mixer-1", m.Data["entity_name"])
			assert.EqualValues(t, 1, m.Data["value"])
		}
	}
	assert.True(t, found, "the lease gauge is exported")
}

// A provider without a writer or with no interval is refused, not started
// to drop every point.
func TestInitMeterProvider_RefusesABadConfig(t *testing.T) {
	_, err := telemetry.InitMeterProvider(telemetry.MeterConfig{Interval: time.Second})
	require.Error(t, err)
	gen, _ := idgen.New(uuid.New())
	w := telemetry.NewNatsWriter(bus.NewMockBus(), uuid.New(), "m", "flux.telemetry", gen, time.Second)
	_, err = telemetry.InitMeterProvider(telemetry.MeterConfig{Writer: w})
	require.Error(t, err)
}
