// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// received collects message values in arrival order.
type received struct {
	mu   sync.Mutex
	vals map[string]int
}

func (r *received) add(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vals[v]++
}

func (r *received) has(vals ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		if r.vals[v] == 0 {
			return false
		}
	}
	return true
}

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		require.True(t, time.Now().Before(deadline), "timed out after %s waiting for %s", within, what)
		time.Sleep(50 * time.Millisecond) // poll interval
	}
}

func valued(v string) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	// The stream drops a message whose ID it has seen: each needs its own.
	m.FluxID = uuid.Must(uuid.NewV7())
	m.Data = map[string]any{"v": v}
	return m
}

func names(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%s-%d", prefix, i))
	}
	return out
}

func subscribeCollect(t *testing.T, nb *NatsBus, subject string) (*liveConsumer, *received) {
	t.Helper()
	got := &received{vals: map[string]int{}}
	sub, err := nb.Subscribe(subject, func(_ context.Context, m *fluxmsg.FluxMsg) error {
		got.add(fmt.Sprint(m.Data["v"]))
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	live, ok := sub.(*liveConsumer)
	require.True(t, ok)
	return live, got
}

// A consumer deleted under a subscription is replaced, and the messages
// published while it was gone still arrive.
func TestSubscribe_ReplacesADeletedConsumer(t *testing.T) {
	nb, js := newTestStream(t)
	live, got := subscribeCollect(t, nb, "flux.live.deleted")
	ctx := context.Background()

	for _, v := range names("before", 1, 3) {
		require.NoError(t, nb.Publish(ctx, "flux.live.deleted", valued(v)))
	}
	waitFor(t, 10*time.Second, "the first messages", func() bool { return got.has(names("before", 1, 3)...) })

	live.mu.Lock()
	first := live.name
	live.mu.Unlock()
	require.NoError(t, js.DeleteConsumer(ctx, "flux", first))
	for _, v := range names("after", 1, 3) {
		require.NoError(t, nb.Publish(ctx, "flux.live.deleted", valued(v)))
	}
	waitFor(t, 60*time.Second, "the messages published after the consumer was deleted",
		func() bool { return got.has(names("after", 1, 3)...) })

	live.mu.Lock()
	defer live.mu.Unlock()
	require.NotEqual(t, first, live.name, "a new consumer replaced the deleted one")
}

// The consumer of a subscription lives on one server. When that server
// stops, the subscription must not stall: the consumer is replaced on a
// surviving server, and no message published meanwhile is lost.
func TestSubscribe_SurvivesTheServerOfItsConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a missed consumer heartbeat")
	}
	servers, urls := startBusCluster(t)
	nb := NewNatsBus("flux-msg")
	require.NoError(t, nb.Connect(strings.Join(urls, ","), ConnectOptions{
		Name: t.Name(), ConnectTimeout: 5 * time.Second, ReconnectWait: 100 * time.Millisecond,
		OperationTimeout: 5 * time.Second,
		ConsumerReplicas: 1, // the single-replica consumer, which is lost with its server
	}))
	t.Cleanup(nb.Close)
	live, got := subscribeCollect(t, nb, "flux.msg.live")
	ctx := context.Background()

	publish := func(v string) {
		// The cluster elects new leaders after a member stops: retry.
		waitFor(t, 60*time.Second, "a publish of "+v, func() bool {
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return nb.Publish(pctx, "flux.msg.live", valued(v)) == nil
		})
	}
	for _, v := range names("before", 1, 3) {
		publish(v)
	}
	waitFor(t, 10*time.Second, "the first messages", func() bool { return got.has(names("before", 1, 3)...) })

	// Stop the server that holds the consumer.
	nc, err := nats.Connect(strings.Join(urls, ","))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	// The consumer can be replaced while the cluster settles: read its
	// current name and host until both answer.
	var host string
	waitFor(t, 30*time.Second, "the server that holds the consumer", func() bool {
		live.mu.Lock()
		name := live.name
		live.mu.Unlock()
		cons, errCons := js.Consumer(ctx, "flux-msg", name)
		if errCons != nil || cons.CachedInfo().Cluster == nil {
			return false
		}
		host = cons.CachedInfo().Cluster.Leader
		return host != ""
	})
	stopped := false
	for _, s := range servers {
		if s.Name() == host {
			s.Shutdown()
			stopped = true
		}
	}
	require.True(t, stopped, "found the server %s that holds the consumer", host)

	for _, v := range names("after", 1, 3) {
		publish(v)
	}
	waitFor(t, 120*time.Second, "the messages published after the consumer's server stopped",
		func() bool { return got.has(names("after", 1, 3)...) })
}

// A consumer that misses its heartbeat twice in a row is replaced even
// when the cluster still lists it: on the latency testbed, a lookup kept
// timing out while the consumer delivered nothing. A delivery resets the
// count, and a single missed heartbeat keeps the consumer.
func TestSubscribe_ReplacesAConsumerThatStaysSilent(t *testing.T) {
	nb, _ := newTestStream(t)
	live, got := subscribeCollect(t, nb, "flux.live.silent")
	name := func() string {
		live.mu.Lock()
		defer live.mu.Unlock()
		return live.name
	}
	first := name()

	missHeartbeat := func(want int32) {
		live.onError(nil, jetstream.ErrNoHeartbeat)
		waitFor(t, 10*time.Second, "the heartbeat check", func() bool { return live.missed.Load() >= want || name() != first })
	}
	missHeartbeat(1)
	require.Equal(t, first, name(), "one missed heartbeat keeps a listed consumer")

	// A delivery resets the count.
	require.NoError(t, nb.Publish(context.Background(), "flux.live.silent", valued("reset")))
	waitFor(t, 10*time.Second, "the delivery", func() bool { return got.has("reset") })
	require.Zero(t, live.missed.Load())

	missHeartbeat(1)
	live.onError(nil, jetstream.ErrNoHeartbeat)
	waitFor(t, 10*time.Second, "the replacement", func() bool { return name() != first })

	require.NoError(t, nb.Publish(context.Background(), "flux.live.silent", valued("after")))
	waitFor(t, 10*time.Second, "a message through the new consumer", func() bool { return got.has("after") })
}

// By default a subscription's consumer has as many replicas as the stream.
// When the server that leads it stops, the consumer stays: its leader moves
// to a surviving member, and every message published afterwards arrives,
// with no replacement.
func TestSubscribe_ReplicatedConsumerSurvivesItsLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a cluster")
	}
	servers, urls := startBusCluster(t)
	nb := NewNatsBus("flux-msg")
	require.NoError(t, nb.Connect(strings.Join(urls, ","), ConnectOptions{
		Name: t.Name(), ConnectTimeout: 5 * time.Second, ReconnectWait: 100 * time.Millisecond,
		OperationTimeout: 5 * time.Second,
	}))
	t.Cleanup(nb.Close)
	live, got := subscribeCollect(t, nb, "flux.msg.replicated")
	ctx := context.Background()
	publish := func(v string) {
		waitFor(t, 60*time.Second, "a publish of "+v, func() bool {
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return nb.Publish(pctx, "flux.msg.replicated", valued(v)) == nil
		})
	}
	for _, v := range names("before", 1, 3) {
		publish(v)
	}
	waitFor(t, 10*time.Second, "the first messages", func() bool { return got.has(names("before", 1, 3)...) })

	nc, err := nats.Connect(strings.Join(urls, ","))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	live.mu.Lock()
	name := live.name
	live.mu.Unlock()
	var host string
	waitFor(t, 30*time.Second, "the consumer's leader", func() bool {
		cons, errCons := js.Consumer(ctx, "flux-msg", name)
		if errCons != nil || cons.CachedInfo().Cluster == nil {
			return false
		}
		require.Equal(t, 3, cons.CachedInfo().Config.Replicas, "the consumer has the stream's replica count")
		host = cons.CachedInfo().Cluster.Leader
		return host != ""
	})
	stopped := false
	for _, s := range servers {
		if s.Name() == host {
			s.Shutdown()
			stopped = true
		}
	}
	require.True(t, stopped, "found the server %s that holds the consumer", host)
	for _, v := range names("after", 1, 3) {
		publish(v)
	}
	waitFor(t, 60*time.Second, "the messages published after the leader stopped",
		func() bool { return got.has(names("after", 1, 3)...) })
	live.mu.Lock()
	defer live.mu.Unlock()
	require.Equal(t, name, live.name, "a replicated consumer is not replaced")
}

// The replica count of a subscription's consumer is read when each consumer
// is created. A stream that cannot be read fails that creation instead of
// giving 0: the server makes an ephemeral consumer created with 0 replicas a
// single replica for its whole life, so one created during an election
// would stay on one server.
func TestSubscriptionReplicas_UnreadableStreamIsAnError(t *testing.T) {
	nb, _ := newTestStream(t)
	nb.streamName = "no-such-stream"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := nb.subscriptionReplicas(ctx)
	require.Error(t, err)
	require.Zero(t, r)
	require.Zero(t, nb.streamReplicas.Load(), "a failed read caches nothing")
}

func TestLiveConsumer_ResolvesReplicasForEachConsumer(t *testing.T) {
	nb, js := newTestStream(t)
	got := &received{vals: map[string]int{}}
	var mu sync.Mutex
	calls := 0
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.replicas",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, 10*time.Millisecond, 5*time.Second, func(msg jetstream.Msg) bool {
		var m fluxmsg.FluxMsg
		if cbor.Unmarshal(msg.Data(), &m) == nil {
			got.add(fmt.Sprint(m.Data["v"]))
		}
		_ = msg.Ack()
		return true
	})
	live.replicas = func(context.Context) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return 0, errors.New("the stream has no leader yet")
		}
		return 1, nil
	}
	require.NoError(t, live.start(3), "the creation is retried after the failed read")
	t.Cleanup(func() { _ = live.Unsubscribe() })

	ctx := context.Background()
	live.mu.Lock()
	first := live.name
	live.mu.Unlock()
	info, err := js.Consumer(ctx, "flux", first)
	require.NoError(t, err)
	require.Equal(t, 1, info.CachedInfo().Config.Replicas)
	mu.Lock()
	require.Equal(t, 2, calls, "one failed read, then the consumer")
	mu.Unlock()

	// A replacement reads the count again.
	require.NoError(t, js.DeleteConsumer(ctx, "flux", first))
	require.NoError(t, nb.Publish(ctx, "flux.live.replicas", valued("after")))
	waitFor(t, 60*time.Second, "a message through the replacement", func() bool { return got.has("after") })
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, calls, 3, "the replacement resolved its replica count")
}

// A subscription still retrying its first consumer stops when the bus
// closes, instead of waiting out every attempt.
func TestLiveConsumer_StartStopsWhenTheBusCloses(t *testing.T) {
	_, js := newTestStream(t)
	closed := make(chan struct{})
	live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
		FilterSubject: "flux.live.closing",
		AckPolicy:     jetstream.AckExplicitPolicy,
	}, time.Hour, 5*time.Second, func(jetstream.Msg) bool { return true })
	live.replicas = func(context.Context) (int, error) { return 0, errors.New("the stream has no leader yet") }
	live.busClosed = closed

	errc := make(chan error, 1)
	go func() { errc <- live.start(100) }()
	close(closed)
	select {
	case err := <-errc:
		require.ErrorContains(t, err, "the bus is closing")
	case <-time.After(10 * time.Second):
		t.Fatal("start kept retrying after the bus closed")
	}
}

// The number of missed heartbeats that replaces a consumer is configured.
func TestLiveConsumer_MissedHeartbeatLimit(t *testing.T) {
	l := &liveConsumer{}
	require.Equal(t, defaultMissedHeartbeats, l.missedLimit())
	l.maxMissed = 5
	require.Equal(t, 5, l.missedLimit())
}

// A consumer kept on Unsubscribe stays on the server for its inactivity
// threshold to remove, and one not kept is deleted at once.
func TestLiveConsumer_KeepLeavesTheConsumer(t *testing.T) {
	_, js := newTestStream(t)
	ctx := context.Background()
	for _, keep := range []bool{true, false} {
		live := newLiveConsumer(js, nil, "flux", "flux-test", jetstream.ConsumerConfig{
			FilterSubject:     "flux.live.keep",
			AckPolicy:         jetstream.AckExplicitPolicy,
			InactiveThreshold: time.Minute,
		}, 10*time.Millisecond, 5*time.Second, func(msg jetstream.Msg) bool {
			_ = msg.Ack()
			return true
		})
		live.keep = keep
		require.NoError(t, live.start(1))
		live.mu.Lock()
		name := live.name
		live.mu.Unlock()
		require.NoError(t, live.Unsubscribe())
		_, err := js.Consumer(ctx, "flux", name)
		if keep {
			require.NoError(t, err, "a kept consumer is still there")
		} else {
			require.ErrorIs(t, err, jetstream.ErrConsumerNotFound, "a consumer not kept is deleted")
		}
	}
}
