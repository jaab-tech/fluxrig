// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// A replace that keeps failing must stop when the bus closes, not retry
// forever: Close never unsubscribes live consumers, so without the busClosed
// select the retry loop and its goroutine leak after Close.
func TestLiveConsumer_ReplaceStopsWhenTheBusCloses(t *testing.T) {
	_, js := newTestStream(t)
	closed := make(chan struct{})
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.replace-closing",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, 10*time.Millisecond, 5*time.Second, func(jetstream.Msg) bool { return true })
	// Every creation fails, so replace retries until the bus closes.
	live.replicas = func(context.Context) (int, error) { return 0, errors.New("the stream has no leader yet") }
	live.busClosed = closed

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		live.replace()
	}()
	close(closed)
	select {
	case <-donec:
	case <-time.After(10 * time.Second):
		t.Fatal("replace kept retrying after the bus closed")
	}
}

// One death can signal both channels: the consume context ends (watch) and
// a heartbeat error fires (onError). Both must be drained before deciding,
// so the consumer is replaced once, not twice.
func TestLiveConsumer_AwaitIncidentDrainsBothSignals(t *testing.T) {
	_, js := newTestStream(t)
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.drain",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, 10*time.Millisecond, 5*time.Second, func(jetstream.Msg) bool { return true })

	live.stopped <- struct{}{}
	live.onError(nil, jetstream.ErrNoHeartbeat)

	ended, ok := live.awaitIncident()
	require.True(t, ok)
	require.True(t, ended, "a pending stop must replace at once")

	select {
	case <-live.stopped:
		t.Fatal("the stop signal was not drained")
	default:
	}
	select {
	case <-live.lost:
		t.Fatal("the heartbeat signal was not drained")
	default:
	}
}

// A lone heartbeat miss is not a stop: it goes through the heartbeat check.
func TestLiveConsumer_AwaitIncidentKeepsALoneHeartbeat(t *testing.T) {
	_, js := newTestStream(t)
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.drain-lost",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, 10*time.Millisecond, 5*time.Second, func(jetstream.Msg) bool { return true })

	live.onError(nil, jetstream.ErrNoHeartbeat)

	ended, ok := live.awaitIncident()
	require.True(t, ok)
	require.False(t, ended, "a heartbeat miss alone must not replace at once")
}

// An ended consume context plus heartbeat misses replaces the consumer
// once: after the first replacement no signal is left to run a second one.
//
// One death signals both channels at once: the consume context ends (watch)
// and a heartbeat error fires (onError). The old supervise handled them in
// separate rounds, and with a miss already counted from an earlier slow
// link the leftover heartbeat signal ran a second replacement for the same
// death. Both signals are queued here before supervise starts, the way one
// death queues them while it is idle, with one miss already counted.
func TestLiveConsumer_OneDeathReplacesOnce(t *testing.T) {
	_, js := newTestStream(t)
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.once",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, 10*time.Millisecond, 5*time.Second, func(m jetstream.Msg) bool {
		_ = m.Ack()
		return true
	})
	t.Cleanup(func() { _ = live.Unsubscribe() })
	cfg := live.base
	cfg.DeliverPolicy = jetstream.DeliverNewPolicy
	require.NoError(t, live.consume(cfg))
	live.mu.Lock()
	name := live.name
	live.mu.Unlock()

	// A miss counted earlier: a slow link, kept on purpose.
	live.missed.Store(1)
	// One death, both signals, queued before supervise runs.
	live.stopped <- struct{}{}
	live.onError(nil, jetstream.ErrNoHeartbeat)
	go live.supervise()

	// Each replacement creates one consumer and never deletes the old one,
	// so the consumer count is one plus the number of replacements: one
	// here, two when the leftover signal runs a second replacement.
	waitFor(t, 30*time.Second, "the replacement", func() bool {
		live.mu.Lock()
		defer live.mu.Unlock()
		return live.name != name
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	stream, err := js.Stream(context.Background(), "flux")
	require.NoError(t, err)
	lister := stream.ConsumerNames(context.Background())
	var names []string
	for name := range lister.Name() {
		names = append(names, name)
	}
	require.NoError(t, lister.Err())
	require.Len(t, names, 2, "one death must create exactly one replacement consumer, got %v", names)
}
