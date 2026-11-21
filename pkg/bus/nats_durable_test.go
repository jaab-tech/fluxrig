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

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// durableStubJS records how SubscribeDurable creates its consumer: which
// replica count it asked for, and whether the create context had a deadline.
type durableStubJS struct {
	jetstream.JetStream
	streamReplicas int
	streamErr      error
	gotReplicas    int
	hasDeadline    bool
}

type durableStubStream struct {
	jetstream.Stream
	replicas int
}

func (s durableStubStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: jetstream.StreamConfig{Replicas: s.replicas}}
}

type durableStubConsumer struct {
	jetstream.Consumer
}

func (durableStubConsumer) CachedInfo() *jetstream.ConsumerInfo {
	return &jetstream.ConsumerInfo{Name: "durable-stub"}
}

func (durableStubConsumer) Consume(jetstream.MessageHandler, ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	return durableStubConsumeContext{}, nil
}

type durableStubConsumeContext struct {
	jetstream.ConsumeContext
}

func (durableStubConsumeContext) Stop() {}

func (s *durableStubJS) Stream(context.Context, string) (jetstream.Stream, error) {
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	return durableStubStream{replicas: s.streamReplicas}, nil
}

func (s *durableStubJS) CreateOrUpdateConsumer(ctx context.Context, _ string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	s.hasDeadline, s.gotReplicas = ctx.Done() != nil, cfg.Replicas
	if _, ok := ctx.Deadline(); ok {
		s.hasDeadline = true
	}
	return durableStubConsumer{}, nil
}

func okHandler(context.Context, *fluxmsg.FluxMsg) error { return nil }

// A durable consumer keeps its replica count for life: creating one without
// a count during an election leaves it a single replica on one server, and
// creating it on an unbounded context holds the subscriber while the cluster
// is unreachable. The create must carry the stream's replica count and a
// deadline from the operation timeout.
func TestNatsBus_SubscribeDurableSetsReplicasWithABoundedContext(t *testing.T) {
	nb := NewNatsBus("flux")
	nb.opTimeout = time.Second
	stub := &durableStubJS{streamReplicas: 3}
	nb.js = stub

	sub, err := nb.SubscribeDurable("flux.msg.durable", "durable-test", okHandler)
	require.NoError(t, err)
	require.NoError(t, sub.Unsubscribe())

	require.Equal(t, 3, stub.gotReplicas, "the durable consumer takes the stream's replica count")
	require.True(t, stub.hasDeadline, "the consumer create is bounded, not Background")
}

// An unreadable stream fails the durable subscribe, as it does for Subscribe:
// creating the consumer without a count would leave it a single replica for
// its whole life.
func TestNatsBus_SubscribeDurableFailsWhenTheStreamIsUnreadable(t *testing.T) {
	nb := NewNatsBus("flux")
	nb.opTimeout = time.Second
	stub := &durableStubJS{streamErr: errors.New("the stream has no leader yet")}
	nb.js = stub

	_, err := nb.SubscribeDurable("flux.msg.durable", "durable-test", okHandler)
	require.ErrorContains(t, err, "no leader")
}
