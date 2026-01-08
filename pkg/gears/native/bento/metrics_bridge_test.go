package bento_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/bento"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestBentoBridge(t *testing.T) {
	// Setup OTel SDK with a reader to capture metrics
	rdr := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(rdr))
	otel.SetMeterProvider(mp)

	// Create Bridge
	ctor := bento.NewBridgeCtor()
	exporter, err := ctor(nil, nil)
	require.NoError(t, err)

	// Test Counter
	cntCtor := exporter.NewCounterCtor("test_counter", "tag")
	cnt := cntCtor("value1")
	cnt.Incr(10)

	// Test Gauge
	gaugeCtor := exporter.NewGaugeCtor("test_gauge")
	gauge := gaugeCtor()
	gauge.Set(42)
	gauge.Set(50) // Delta should be +8

	// Test Timer
	timerCtor := exporter.NewTimerCtor("test_timer")
	timer := timerCtor()
	timer.Timing(1000) // 1000ns

	// Collect
	var data metricdata.ResourceMetrics
	err = rdr.Collect(context.Background(), &data)
	require.NoError(t, err)

	// Verify
	foundCounter := false
	foundGauge := false
	foundTimer := false

	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name == "bento.internal" {
			for _, m := range scope.Metrics {
				switch m.Name {
				case "bento.test_counter":
					sum, ok := m.Data.(metricdata.Sum[int64])
					if ok {
						assert.Equal(t, int64(10), sum.DataPoints[0].Value)
						foundCounter = true
					}
				case "bento.test_gauge":
					sum, ok := m.Data.(metricdata.Sum[int64])
					if ok {
						// Gauge simulated via UpDownCounter, so it's a Sum
						assert.Equal(t, int64(50), sum.DataPoints[0].Value)
						foundGauge = true
					}
				case "bento.test_timer":
					hist, ok := m.Data.(metricdata.Histogram[float64])
					if ok {
						assert.Equal(t, uint64(1), hist.DataPoints[0].Count)
						assert.Equal(t, float64(1000), hist.DataPoints[0].Sum)
						foundTimer = true
					}
				}
			}
		}
	}

	assert.True(t, foundCounter, "Counter not found")
	assert.True(t, foundGauge, "Gauge not found")
	assert.True(t, foundTimer, "Timer not found")
}
