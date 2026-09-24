// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

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
	InsecureSkipVerify        bool          // Optional bypass for local testing
	InProcessServer           any           // Optional direct NATS server instance (In-Process)
	InactiveThreshold         time.Duration // Time after which the NATS server cleans up idle consumers
	InitialRetryWait          time.Duration // Base wait time for initial connection retries
	InitialRetryAttempts      int           // Number of initial connection retry attempts
	InitialRetryTimeout       time.Duration // Upper bound on the total time spent retrying the first connection; 0 means only the attempt count limits it
	// Ctx bounds the initial connection retry: a caller with a shutdown signal
	// (SIGTERM/SIGINT) passes its context here so that signal is observed
	// between retry attempts instead of only after the whole retry budget
	// elapses. Nil means only InitialRetryAttempts/InitialRetryTimeout bound it.
	Ctx context.Context
}

// Bus defines the standard behavior for our messaging layer.
// By using an interface, we can swap NATS for a memory mock during tests.
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

// Handler is the function signature for processing incoming messages.
// The context contains tracing information extracted from the message.
type Handler func(ctx context.Context, msg *fluxmsg.FluxMsg)

// RawHandler is the function signature for processing raw incoming messages.
type RawHandler func(ctx context.Context, subject string, data []byte)

// Subscription represents an active listener.
type Subscription interface {
	Unsubscribe() error
}
