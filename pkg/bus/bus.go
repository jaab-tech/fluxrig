// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// ErrInvalidMessage marks a publish that failed because of the message itself:
// it fails validation, cannot be encoded, or exceeds the server's payload
// limit. Sending it again cannot succeed, on this bus or on any other. Every
// other publish error says nothing about the message, and a caller must treat
// it as "the bus did not take it", not as "the message is bad".
var ErrInvalidMessage = errors.New("bus: invalid message")

// ConnectOptions holds parameters for establishing a connection.
type ConnectOptions struct {
	Name                      string
	Domain                    string
	ConnectTimeout            time.Duration
	ReconnectWait             time.Duration
	OperationTimeout          time.Duration
	SubscriptionRetryWait     time.Duration
	SubscriptionRetryAttempts int
	RootCA                    string        // Optional path to Root CA for TLS
	ClientCert                string        // Optional certificate this client presents
	ClientKey                 string        // Key of ClientCert
	Credentials               string        // Optional NATS credentials file
	InboxPrefix               string        // Optional prefix of this client's reply inboxes; empty keeps _INBOX
	PingInterval              time.Duration // How often the client pings its server; zero keeps the client default (see Security)
	MaxPingsOut               int           // Unanswered pings before the client reconnects; zero keeps the client default
	AckWait                   time.Duration // How long a guaranteed-lane or durable consumer waits for an ack before it delivers again; zero keeps the server default
	External                  bool          // NATS is the operator's: verify the host of the URL, not the domain
	InsecureSkipVerify        bool          // Optional bypass for local testing
	InProcessServer           any           // Optional direct NATS server instance (In-Process)
	InactiveThreshold         time.Duration // Time after which the NATS server cleans up idle consumers
	KeepConsumers             bool          // Leave an ended subscription's ephemeral consumer to InactiveThreshold: a NATS that limits a Rack cannot let it delete consumers, whose names carry no Rack
	InitialRetryWait          time.Duration // Base wait time for initial connection retries
	InitialRetryAttempts      int           // Number of initial connection retry attempts
	InitialRetryTimeout       time.Duration // Upper bound on the total time spent retrying the first connection; 0 means only the attempt count limits it
	// Ctx bounds the initial connection retry: a caller with a shutdown signal
	// (SIGTERM/SIGINT) passes its context here so that signal is observed
	// between retry attempts instead of only after the whole retry budget
	// elapses. Nil means only InitialRetryAttempts/InitialRetryTimeout bound it.
	Ctx context.Context
	// DLQ routes a message no consumer could process to a dead letter stream
	// instead of dropping it. DLQEnabled is the switch; DLQSubject is the
	// subject prefix the dead letter stream captures. Disabled keeps the
	// previous behavior: log and discard.
	DLQEnabled bool
	DLQSubject string
	// DLQPublishTimeout bounds one dead letter publish. Zero or less selects
	// the default (5s).
	DLQPublishTimeout time.Duration
	// FailoverAfter is how many JetStream publishes in a row may find no
	// stream (a timeout or no responders) before the bus drops its server and
	// reconnects to another one in the URL list. A server cut off from its
	// cluster keeps the client connection open while every publish through it
	// fails, and the client never leaves it on its own. Zero disables it.
	FailoverAfter int
	// ConsumerReplicas is the replica count of an acknowledged
	// subscription's consumer. 0 takes the stream's replica count.
	ConsumerReplicas int
	// ConsumerMissedHeartbeats is how many heartbeats in a row a
	// subscription's consumer may miss before it is replaced. 0 takes 2.
	ConsumerMissedHeartbeats int
}

// Bus defines the standard behavior for our messaging layer.
// By using an interface, we can swap NATS for a memory mock during tests.
// RawMessage is one pre-serialized message of a batch, with its
// deduplication ID.
type RawMessage struct {
	Data []byte
	ID   uuid.UUID
}

// BatchRawPublisher is an optional capability of a Bus: it publishes several
// messages on one subject without waiting for each acknowledgement before it
// sends the next. Across a slow link one publish costs a round trip to the
// stream leader, so one at a time a Rack moved about one message per round
// trip. It returns how many of the first messages the stream took, in order,
// and the error of the first one it did not. A message the stream took after
// that one is taken again harmlessly on a resend: it has its deduplication ID.
type BatchRawPublisher interface {
	PublishRawBatch(ctx context.Context, subject string, msgs []RawMessage) (int, error)
}

// PublishRawBatch publishes msgs through b in one batch when b can, and one at
// a time otherwise.
func PublishRawBatch(ctx context.Context, b Bus, subject string, msgs []RawMessage) (int, error) {
	if bp, ok := b.(BatchRawPublisher); ok {
		return bp.PublishRawBatch(ctx, subject, msgs)
	}
	for i, m := range msgs {
		if err := b.PublishRaw(ctx, subject, m.Data, m.ID); err != nil {
			return i, err
		}
	}
	return len(msgs), nil
}

type Bus interface {
	// Connect establishes the connection to the mesh.
	Connect(url string, opts ConnectOptions) error

	// Publish sends a FluxMsg to a specific subject (topic).
	// It must respect the context for cancellation and tracing propagation.
	Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error

	// PublishRaw sends pre-serialized data (CBOR) with a specific deduplication ID.
	PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error

	// Subscribe listens for messages on a subject.
	// It returns a subscription object (to allow Unsubscribe) and an error.
	Subscribe(subject string, handler Handler) (Subscription, error)

	// SubscribeRaw listens for raw messages (bytes) on a subject.
	// Useful for integrating with non-FluxMsg streams (e.g. NATS KV, distinct protocols).
	SubscribeRaw(subject string, streamName string, handler RawHandler) (Subscription, error)

	// SubscribeDurable listens for messages using a persistent consumer (durableName).
	// This ensures messages are not lost/duplicated across restarts.
	SubscribeDurable(subject, durableName string, handler Handler) (Subscription, error)

	// KV returns the KeyValue interface for distributed state management.
	KV() KeyValue

	// Core returns the underlying core NATS connection (if applicable).
	Core() any

	// Close cleans up the connection.
	Close()
}

// Handler is the function signature for processing incoming messages. The
// context contains tracing information extracted from the message. A guaranteed
// subscription (Subscribe, SubscribeDurable) acks only once the handler returns
// nil, and Naks on a non-nil error so JetStream redelivers instead of the
// message vanishing on a failure that happened after it was already
// acknowledged.
type Handler func(ctx context.Context, msg *fluxmsg.FluxMsg) error

// RawHandler is the function signature for processing raw incoming messages.
type RawHandler func(ctx context.Context, subject string, data []byte)

// Subscription represents an active listener.
type Subscription interface {
	Unsubscribe() error
}
