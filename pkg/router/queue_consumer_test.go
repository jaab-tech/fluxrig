// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// queueMember is one Mixer's router in a shared queue group.
func queueMember(t *testing.T, url string) *RouterWrapper {
	t.Helper()
	logger := watermill.NopLogger{}
	r, err := NewRouter(logger)
	require.NoError(t, err)
	require.NoError(t, r.ConfigureJetStream(JetStreamSetup{
		URL: url, Durable: true,
		BusinessStream: "flux-msg", BusinessSubjects: []string{"flux.msg.>", "flux.agent.>"},
		TelemetryStream: "flux-telemetry", TelemetrySubjects: []string{"flux.telemetry.>"},
		QueueGroup: "fluxrig-mixers",
	}, logger))
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// A member that stops cleanly does not take the shared consumer with it, and
// a member that joins does not replay what was published before.
func TestQueueGroup_ConsumerSurvivesACleanStop(t *testing.T) {
	snk, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "queue-test", StoreDir: t.TempDir()})
	require.NoError(t, err)
	defer snk.Shutdown()

	const topic = "flux.agent.hello"
	a := queueMember(t, snk.ClientURL())
	b := queueMember(t, snk.ClientURL())

	// Published before anyone subscribed: must not be replayed.
	require.NoError(t, a.Pub.Publish(topic, message.NewMessage(watermill.NewUUID(), []byte("old"))))

	msgsA, err := a.Sub.Subscribe(context.Background(), topic)
	require.NoError(t, err)
	msgsB, err := b.Sub.Subscribe(context.Background(), topic)
	require.NoError(t, err)

	var mu sync.Mutex
	got := map[string]int{}
	collect := func(ch <-chan *message.Message) {
		for m := range ch {
			mu.Lock()
			got[string(m.Payload)]++
			mu.Unlock()
			m.Ack()
		}
	}
	go collect(msgsA)
	go collect(msgsB)

	// A stops cleanly. With nats.go owning the consumer, this deleted it.
	require.NoError(t, a.Sub.Close())

	nc, err := nats.Connect(snk.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.ConsumerInfo("flux-msg", queueDurableName("fluxrig-mixers", topic))
	require.NoError(t, err, "the shared consumer outlives the member that stopped")

	const n = 5
	for i := 0; i < n; i++ {
		require.NoError(t, b.Pub.Publish(topic, message.NewMessage(watermill.NewUUID(), []byte(fmt.Sprintf("new-%d", i)))))
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		count := 0
		for i := 0; i < n; i++ {
			count += got[fmt.Sprintf("new-%d", i)]
		}
		mu.Unlock()
		if count == n {
			break
		}
		require.True(t, time.Now().Before(deadline), "the remaining member received %d of %d", count, n)
		time.Sleep(20 * time.Millisecond) // poll interval, not a wait for the condition
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, got["old"], "a message published before the group existed is not replayed")
}

// flakySubscriber fails its first n subscribes the way a forming cluster does.
type flakySubscriber struct {
	message.Subscriber
	mu    sync.Mutex
	fails int
	calls int
}

func (f *flakySubscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	f.mu.Lock()
	f.calls++
	failing := f.calls <= f.fails
	f.mu.Unlock()
	if failing {
		return nil, fmt.Errorf("cannot subscribe: %w", context.DeadlineExceeded)
	}
	return f.Subscriber.Subscribe(ctx, topic)
}

// A subscribe that times out while the cluster forms is tried again until the
// form timeout passes, instead of ending the router and the Mixer with it.
func TestQueueGroup_SubscribeRetriesWhileTheClusterForms(t *testing.T) {
	snk, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "queue-retry", StoreDir: t.TempDir()})
	require.NoError(t, err)
	defer snk.Shutdown()

	r := queueMember(t, snk.ClientURL())
	qs, ok := r.Sub.(*queueConsumerSubscriber)
	require.True(t, ok)
	flaky := &flakySubscriber{Subscriber: qs.Subscriber, fails: 2}
	qs.Subscriber, qs.retryWait = flaky, 10*time.Millisecond

	_, err = r.Sub.Subscribe(context.Background(), "flux.agent.hello")
	require.NoError(t, err)
	assert.Equal(t, 3, flaky.calls, "two failed attempts, then one that succeeds")

	// Past the form timeout the error is returned.
	qs.formTimeout = 0
	flaky.calls, flaky.fails = 0, 1
	_, err = r.Sub.Subscribe(context.Background(), "flux.agent.heartbeat")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, flaky.calls)
}

// The queue group's consumer takes snake.ack_wait: a heartbeat pushed to a
// Mixer that was cut off comes back after it, not after the 30s the server
// would choose, and a Rack does not read offline meanwhile.
func TestQueueGroup_ConsumerTakesTheAckWait(t *testing.T) {
	snk, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "queue-ack", StoreDir: t.TempDir()})
	require.NoError(t, err)
	defer snk.Shutdown()
	logger := watermill.NopLogger{}
	r, err := NewRouter(logger)
	require.NoError(t, err)
	require.NoError(t, r.ConfigureJetStream(JetStreamSetup{
		URL: snk.ClientURL(), Durable: true,
		BusinessStream: "flux-msg", BusinessSubjects: []string{"flux.msg.>", "flux.agent.>"},
		TelemetryStream: "flux-telemetry", TelemetrySubjects: []string{"flux.telemetry.>"},
		QueueGroup: "fluxrig-mixers", AckWait: 7 * time.Second,
	}, logger))
	t.Cleanup(func() { _ = r.Close() })
	const topic = "flux.agent.heartbeat"
	_, err = r.Sub.Subscribe(context.Background(), topic)
	require.NoError(t, err)

	nc, err := nats.Connect(snk.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	info, err := js.ConsumerInfo("flux-msg", queueDurableName("fluxrig-mixers", topic))
	require.NoError(t, err)
	require.Equal(t, 7*time.Second, info.Config.AckWait)
}
