// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/nats-io/nats.go"
)

// queueDurableName is the durable consumer name for one topic of a shared
// queue group. It must be unique per topic: one durable over two subjects on
// one stream is rejected with "subject does not match consumer".
func queueDurableName(prefix, topic string) string {
	return prefix + "_" + strings.NewReplacer(".", "_", "*", "_", ">", "_").Replace(topic)
}

// queueConsumerSubscriber creates the shared durable consumer for a topic
// before the Watermill subscriber attaches to it.
//
// nats.go deletes a consumer on Unsubscribe when it created that consumer
// itself, and Watermill unsubscribes on Close. Left to nats.go, the first
// Mixer to subscribe owned the queue group's consumer, and its clean
// shutdown deleted it: every other Mixer then received no Hello and no
// heartbeat. A consumer that already exists is attached to, never deleted,
// so it survives any member leaving.
//
// The consumer starts at new messages. Starting at the first message would
// replay the whole stream (24 h of Hellos and heartbeats) the first time a
// cluster turns the queue group on.
type queueConsumerSubscriber struct {
	message.Subscriber
	nc      *nats.Conn
	group   string
	timeout time.Duration
	// formTimeout bounds the retries of one Subscribe while the cluster
	// forms; retryWait is the pause between two attempts.
	formTimeout time.Duration
	retryWait   time.Duration
	// ackWait is the consumer's ack wait; zero keeps the server default.
	ackWait time.Duration
	// replicas is how many copies of the consumer to keep. Zero keeps the
	// server default (one): set it from setup, so the shared consumer
	// survives a member dying like the streams do.
	replicas int
	logger   watermill.LoggerAdapter
}

// Subscribe ensures the topic's consumer exists, then subscribes. On a
// cluster that is still forming either step can time out, so it tries again
// until formTimeout passes. A Mixer whose router failed its first subscribe
// used to exit, which left a three-Mixer cluster without a quorum the next
// time a member stopped.
func (s *queueConsumerSubscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	deadline := time.Now().Add(s.formTimeout)
	for attempt := 1; ; attempt++ {
		ch, err := s.subscribeOnce(ctx, topic)
		if err == nil {
			return ch, nil
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		if s.logger != nil {
			s.logger.Info("Subscription is waiting for the cluster", watermill.LogFields{
				"topic": topic, "attempt": attempt, "error": err,
			})
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(s.retryWait):
		}
	}
}

func (s *queueConsumerSubscriber) subscribeOnce(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if err := s.ensureConsumer(ctx, topic); err != nil {
		return nil, err
	}
	return s.Subscriber.Subscribe(ctx, topic)
}

func (s *queueConsumerSubscriber) ensureConsumer(ctx context.Context, topic string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	js, err := s.nc.JetStream(nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("router: queue consumer %s: %w", topic, err)
	}
	stream, err := js.StreamNameBySubject(topic, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("router: queue consumer %s: find stream: %w", topic, err)
	}
	durable := queueDurableName(s.group, topic)
	_, errInfo := js.ConsumerInfo(stream, durable, nats.Context(ctx))
	if errInfo == nil {
		return nil // already there: created by another member, or by us earlier
	}
	if !errors.Is(errInfo, nats.ErrConsumerNotFound) {
		return fmt.Errorf("router: queue consumer %s: lookup: %w", durable, errInfo)
	}
	_, err = js.AddConsumer(stream, queueConsumerConfig(s.group, topic, s.ackWait, s.replicas), nats.Context(ctx))
	if err != nil && !errors.Is(err, nats.ErrConsumerNameAlreadyInUse) {
		// Another member creating it at the same moment is fine.
		if _, errInfo := js.ConsumerInfo(stream, durable, nats.Context(ctx)); errInfo == nil {
			return nil
		}
		return fmt.Errorf("router: queue consumer %s: create: %w", durable, err)
	}
	return nil
}

// queueConsumerConfig builds the shared durable consumer for one topic: a
// durable per topic, delivering only new messages to the queue group, with
// the replica count of the streams. Replicas 0 or below keeps the server
// default (one copy); a shared consumer on one copy stops delivering Hellos
// and heartbeats while its member is down.
func queueConsumerConfig(group, topic string, ackWait time.Duration, replicas int) *nats.ConsumerConfig {
	cfg := &nats.ConsumerConfig{
		Durable:        queueDurableName(group, topic),
		DeliverSubject: nats.NewInbox(),
		DeliverGroup:   group,
		FilterSubject:  topic,
		DeliverPolicy:  nats.DeliverNewPolicy,
		AckPolicy:      nats.AckExplicitPolicy,
		AckWait:        ackWait,
	}
	if replicas > 0 {
		cfg.Replicas = replicas
	}
	return cfg
}
