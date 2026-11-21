// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collect(t *testing.T, r *sdkmetric.ManualReader) map[string][]metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, r.Collect(context.Background(), &rm))
	out := map[string][]metricdata.DataPoint[int64]{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				out[m.Name] = d.DataPoints
			case metricdata.Sum[int64]:
				out[m.Name] = d.DataPoints
			}
		}
	}
	return out
}

// The gauge follows whether this Mixer holds the lease, and the counter
// counts each transition by event.
func TestLeaseMetrics(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r))
	held := false
	m := &leaseMetrics{}
	require.NoError(t, m.register(mp.Meter("test"), func() bool { return held }))

	got := collect(t, r)
	require.Len(t, got["flux.mixer.lease.held"], 1)
	assert.EqualValues(t, 0, got["flux.mixer.lease.held"][0].Value)

	held = true
	m.record("acquired")
	m.record("renew_failed")
	m.record("acquired")
	m.record("released")
	m.record("renew") // not a transition
	got = collect(t, r)
	assert.EqualValues(t, 1, got["flux.mixer.lease.held"][0].Value)
	byEvent := map[string]int64{}
	for _, p := range got["flux.mixer.lease.transitions"] {
		ev, _ := p.Attributes.Value("event")
		byEvent[ev.AsString()] = p.Value
	}
	assert.Equal(t, map[string]int64{"acquired": 2, "lost": 1, "released": 1}, byEvent)
}
