// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// The shared queue consumer carries the streams' replica count: a consumer
// created with one copy stops delivering Hellos and heartbeats while its
// member is down.
func TestQueueConsumerConfig_TakesTheStreamReplicas(t *testing.T) {
	cfg := queueConsumerConfig("fluxrig-mixers", "flux.agent.hello", 7*time.Second, 3)
	require.Equal(t, 3, cfg.Replicas)
	require.Equal(t, "fluxrig-mixers_flux_agent_hello", cfg.Durable)
	require.Equal(t, "fluxrig-mixers", cfg.DeliverGroup)
	require.Equal(t, "flux.agent.hello", cfg.FilterSubject)
	require.Equal(t, 7*time.Second, cfg.AckWait)

	// Zero or less keeps the server default: only setup passes a count.
	require.Zero(t, queueConsumerConfig("g", "flux.agent.hello", 0, 0).Replicas)
}

// ConfigureJetStream wires the stream replicas into the shared consumer
// subscriber, the same count the streams themselves are created with. A
// single test server only places one copy, so this asserts the wiring with
// one; the count above one is covered by TestQueueConsumerConfig, which
// builds the exact config ensureConsumer sends.
func TestQueueGroup_ConsumerTakesTheSetupReplicas(t *testing.T) {
	snk, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "queue-replicas", StoreDir: t.TempDir()})
	require.NoError(t, err)
	defer snk.Shutdown()

	logger := watermill.NopLogger{}
	r, err := NewRouter(logger)
	require.NoError(t, err)
	require.NoError(t, r.ConfigureJetStream(JetStreamSetup{
		URL: snk.ClientURL(), Durable: true,
		BusinessStream: "flux-msg", BusinessSubjects: []string{"flux.msg.>", "flux.agent.>"},
		TelemetryStream: "flux-telemetry", TelemetrySubjects: []string{"flux.telemetry.>"},
		QueueGroup: "fluxrig-mixers", Replicas: 1,
	}, logger))
	t.Cleanup(func() { _ = r.Close() })

	qs, ok := r.Sub.(*queueConsumerSubscriber)
	require.True(t, ok, "a queue group subscribes through the shared consumer subscriber")
	require.Equal(t, 1, qs.replicas, "the shared consumer is created with the streams' replica count")
}
