// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// capturingBus captures the raw handler the wrapper registers, so a test can
// deliver a raw message the way the bus would, without a real server.
type capturingBus struct {
	*bus.MockBus
	rawHandler bus.RawHandler
}

func (b *capturingBus) SubscribeRaw(subject, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	b.rawHandler = handler
	return &bus.MockSubscription{}, nil
}

// installMetrics wires a ManualReader-backed Metrics into the package global so
// the bus wrapper records into instruments this test can read back.
func installMetrics(t *testing.T) *metric.ManualReader {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	ms, err := NewMetrics(mp.Meter("bus-wrapper-test"))
	require.NoError(t, err)

	prev := currentMetrics
	currentMetrics = ms
	t.Cleanup(func() { currentMetrics = prev })
	return reader
}

// sumFor returns the accumulated value of an int64 counter by name, or 0.
func sumFor(t *testing.T, reader *metric.ManualReader, name string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	return 0
}

// The raw subscription path used to delegate with no instrumentation, so the
// Coat Check daemon's consumed messages were invisible. It must now advance the
// receive count and the byte total.
func TestInstrumentedBus_SubscribeRawRecordsReceiveMetrics(t *testing.T) {
	fake := &capturingBus{MockBus: bus.NewMockBus()}
	reader := installMetrics(t)
	ibus := NewInstrumentedBus(fake, nil)

	var got []byte
	_, err := ibus.SubscribeRaw("flux.test.>", "stream", func(ctx context.Context, subject string, data []byte) {
		got = append(got, data...)
	})
	require.NoError(t, err)
	require.NotNil(t, fake.rawHandler, "the wrapper must pass a handler to the bus")

	// The bus delivers two messages; the wrapper's handler is what runs.
	fake.rawHandler(context.Background(), "flux.test.a", []byte("hello"))
	fake.rawHandler(context.Background(), "flux.test.b", []byte("world!"))

	assert.Equal(t, "helloworld!", string(got), "the caller's handler still receives the data")
	assert.Equal(t, int64(2), sumFor(t, reader, "flux.nats.messages_received"))
	assert.Equal(t, int64(11), sumFor(t, reader, "flux.nats.bytes_received"))
}

// PublishRaw records the byte size it actually wrote.
func TestInstrumentedBus_PublishRawRecordsBytes(t *testing.T) {
	fake := &capturingBus{MockBus: bus.NewMockBus()}
	reader := installMetrics(t)
	ibus := NewInstrumentedBus(fake, nil)

	// The mock decodes the payload, so it must be a valid CBOR message.
	payload, err := cbor.Marshal(fluxmsg.New())
	require.NoError(t, err)

	require.NoError(t, ibus.PublishRaw(context.Background(), "flux.test.raw", payload, uuid.New()))
	assert.Equal(t, int64(len(payload)), sumFor(t, reader, "flux.nats.bytes_published"))
	assert.Equal(t, int64(1), sumFor(t, reader, "flux.nats.messages_published"))
}

// With no metrics installed the wrapper must pass the message through and not
// panic, whether or not there are instruments to record into.
func TestInstrumentedBus_SubscribeRawWithoutMetrics(t *testing.T) {
	prev := currentMetrics
	currentMetrics = nil
	t.Cleanup(func() { currentMetrics = prev })

	fake := &capturingBus{MockBus: bus.NewMockBus()}
	ibus := NewInstrumentedBus(fake, nil)

	var called bool
	_, err := ibus.SubscribeRaw("flux.test.>", "stream", func(ctx context.Context, subject string, data []byte) {
		called = true
	})
	require.NoError(t, err)
	fake.rawHandler(context.Background(), "flux.test.a", []byte("x"))
	assert.True(t, called, "the handler must run with no metrics installed")
}
