// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// newTestStream starts an embedded NATS server with a JetStream stream named
// "flux" over "flux.>", and returns a connected NatsBus plus a raw jetstream
// handle for assertions the Bus interface does not expose.
func newTestStream(t *testing.T) (*NatsBus, jetstream.JetStream) {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{
		Port:        -1,
		ClusterName: t.Name(),
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)

	nb := NewNatsBus("flux")
	require.NoError(t, nb.Connect(s.ClientURL(), ConnectOptions{
		Name:           t.Name(),
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  1 * time.Second,
	}))
	t.Cleanup(nb.Close)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "flux",
		Subjects: []string{"flux.>"},
	})
	require.NoError(t, err)

	return nb, js
}

// TestNatsBus_SubscribeRedeliversOnHandlerFailure is a regression test for the
// BLOCKER finding: Subscribe acked before CBOR decode and before the handler ran,
// so any failure after that point was unrecoverable — at-most-once delivery
// disguised as the guaranteed lane it claimed to be. A handler that fails must now
// get the same message again, not lose it.
func TestNatsBus_SubscribeRedeliversOnHandlerFailure(t *testing.T) {
	nb, _ := newTestStream(t)

	var attempts atomic.Int32
	succeeded := make(chan struct{})
	sub, err := nb.Subscribe("flux.retry.test", func(ctx context.Context, m *fluxmsg.FluxMsg) error {
		if attempts.Add(1) < 3 {
			return errors.New("synthetic failure")
		}
		close(succeeded)
		return nil
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	require.NoError(t, nb.Publish(context.Background(), "flux.retry.test", fluxmsg.New()))

	select {
	case <-succeeded:
	case <-time.After(10 * time.Second):
		t.Fatalf("message was never redelivered after Nak: got %d attempts", attempts.Load())
	}
	assert.EqualValues(t, 3, attempts.Load(), "the same message must be redelivered, not a new one")
}

// TestNatsBus_SubscribeStopsRedeliveringAfterMaxDeliver is a regression test for
// the other half of the same finding: Nak alone, with no bound, retries a
// deterministically failing handler forever. maxGuaranteedRedeliveries caps it.
func TestNatsBus_SubscribeStopsRedeliveringAfterMaxDeliver(t *testing.T) {
	original := maxGuaranteedRedeliveries
	maxGuaranteedRedeliveries = 2
	defer func() { maxGuaranteedRedeliveries = original }()

	nb, _ := newTestStream(t)

	var attempts atomic.Int32
	sub, err := nb.Subscribe("flux.giveup.test", func(ctx context.Context, m *fluxmsg.FluxMsg) error {
		attempts.Add(1)
		return errors.New("always fails")
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	require.NoError(t, nb.Publish(context.Background(), "flux.giveup.test", fluxmsg.New()))

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 10*time.Second, 20*time.Millisecond,
		"the handler must be retried up to the redelivery limit")

	// Give the server every chance to redeliver past the limit if the bound were
	// not actually enforced, then confirm it did not.
	require.Never(t, func() bool { return attempts.Load() > 2 }, time.Second, 50*time.Millisecond,
		"redelivery must stop at maxGuaranteedRedeliveries, not continue forever")
}

// TestNatsBus_SubscribeDurableStopsRedeliveringAfterMaxDeliver is a regression
// test for the same poison-message gap on the other guaranteed-lane consumer:
// SubscribeDurable's ConsumerConfig set no MaxDeliver at all, so a handler
// that fails every time retried the message forever, unlike Subscribe's
// bounded ephemeral consumer.
func TestNatsBus_SubscribeDurableStopsRedeliveringAfterMaxDeliver(t *testing.T) {
	original := maxGuaranteedRedeliveries
	maxGuaranteedRedeliveries = 2
	defer func() { maxGuaranteedRedeliveries = original }()

	nb, _ := newTestStream(t)

	var attempts atomic.Int32
	sub, err := nb.SubscribeDurable("flux.giveup.durable.test", "giveup-durable", func(ctx context.Context, m *fluxmsg.FluxMsg) error {
		attempts.Add(1)
		return errors.New("always fails")
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	require.NoError(t, nb.Publish(context.Background(), "flux.giveup.durable.test", fluxmsg.New()))

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 10*time.Second, 20*time.Millisecond,
		"the handler must be retried up to the redelivery limit")

	require.Never(t, func() bool { return attempts.Load() > 2 }, time.Second, 50*time.Millisecond,
		"redelivery must stop at maxGuaranteedRedeliveries, not continue forever")
}

// TestNatsBus_SubscribeDiscardsAMessageThatFailsValidation is a regression test
// for the MAJOR finding that ingress never called Validate()/checked MaxHops:
// only Publish did, so a message arriving from another Rack (which never went
// through this process's own Publish) reached the handler unchecked.
func TestNatsBus_SubscribeDiscardsAMessageThatFailsValidation(t *testing.T) {
	nb, js := newTestStream(t)

	called := make(chan struct{}, 1)
	sub, err := nb.Subscribe("flux.invalid.test", func(ctx context.Context, m *fluxmsg.FluxMsg) error {
		called <- struct{}{}
		return nil
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	// Bypass NatsBus.Publish (which would itself refuse this) to simulate a
	// message that arrived already encoded, the way one from another Rack does.
	bad := fluxmsg.New()
	for i := 0; i <= fluxmsg.MaxHops; i++ { // one more hop than MaxHops allows
		bad.Path = append(bad.Path, &fluxmsg.Hop{})
	}
	data, err := cbor.Marshal(bad)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), "flux.invalid.test", data)
	require.NoError(t, err)

	select {
	case <-called:
		t.Fatal("the handler ran on a message that exceeds MaxHops")
	case <-time.After(500 * time.Millisecond):
	}
}

// TestNatsBus_PublishInjectsTraceBeforeMarshal is a regression test for the MAJOR
// finding that Publish marshaled the message to CBOR before injecting the OTel
// trace context into its Metadata: the injected context landed in msg.Metadata in
// memory but never in the bytes actually sent, so it never reached the wire.
func TestNatsBus_PublishInjectsTraceBeforeMarshal(t *testing.T) {
	originalProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(originalProp)

	originalTP := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider() // AlwaysSample by default: a real, valid SpanContext with no exporter needed
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(originalTP)

	nb, js := newTestStream(t)

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	require.True(t, span.SpanContext().IsValid(), "the test's own span must be valid or this test proves nothing")
	defer span.End()

	require.NoError(t, nb.Publish(ctx, "flux.trace.test", fluxmsg.New()))

	// Read the raw stored bytes directly, bypassing Subscribe's own Extract, so
	// this checks what was actually marshaled and sent, not what Subscribe
	// happens to reconstruct afterward.
	stream, err := js.Stream(context.Background(), "flux")
	require.NoError(t, err)
	raw, err := stream.GetLastMsgForSubject(context.Background(), "flux.trace.test")
	require.NoError(t, err)

	var stored fluxmsg.FluxMsg
	require.NoError(t, cbor.Unmarshal(raw.Data, &stored))
	assert.Contains(t, stored.Metadata, "traceparent",
		"the trace context injected before Publish must be in the bytes that were marshaled and sent")
}
