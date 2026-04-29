// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// NatsBus is the concrete implementation of the Bus interface using NATS JetStream.
type NatsBus struct {
	conn          *nats.Conn
	js            jetstream.JetStream
	streamName    string
	retryWait     time.Duration
	retryAttempts int
}

// NewNatsBus creates a new instance.
func NewNatsBus(streamName string) *NatsBus {
	return &NatsBus{
		streamName: streamName,
	}
}

// Connect establishes the connection to the NATS server and initializes JetStream.
func (n *NatsBus) Connect(url string, opts ConnectOptions) error {
	// 1. Build Options
	natsOpts := []nats.Option{
		nats.Name(opts.Name),
		nats.Timeout(opts.ConnectTimeout),
		nats.ReconnectWait(opts.ReconnectWait),
		nats.MaxReconnects(-1), // Infinite reconnects
	}

	// --- Advanced TLS Configuration ---
	if opts.InsecureSkipVerify || opts.RootCA != "" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // allowed for dev/test environments
			MinVersion:         tls.VersionTLS12,
		}

		if opts.RootCA != "" {
			caCert, err := os.ReadFile(opts.RootCA)
			if err != nil {
				return fmt.Errorf("failed to read root ca: %w", err)
			}
			caCertPool := x509.NewCertPool()
			if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
				return fmt.Errorf("failed to append root ca (no PEM blocks found)")
			}
			tlsConfig.RootCAs = caCertPool
		}

		if opts.Domain != "" {
			// If we have a domain (flux), we might want to use it as ServerName for verification
			// But only if the cert supports it (it does in 08_tls_simple)
			tlsConfig.ServerName = opts.Domain
		}

		natsOpts = append(natsOpts, nats.Secure(tlsConfig))
	}

	// 2. Connect to NATS Core
	nc, err := nats.Connect(url, natsOpts...)
	if err != nil {
		return err
	}
	n.conn = nc

	n.retryWait = opts.SubscriptionRetryWait
	n.retryAttempts = opts.SubscriptionRetryAttempts
	if n.retryAttempts <= 0 {
		n.retryAttempts = 1 // At least one attempt
	}

	// 3. Initialize JetStream
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("failed to initialize jetstream: %w", err)
	}
	n.js = js

	return nil
}

// Publish serializes the FluxMsg to CBOR bytes and sends it via JetStream.
func (n *NatsBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// 1. Technical Enforcement (Transparent UTF-8 Guard)
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("fluxmsg validation failed: %w", err)
	}

	// 2. Serialize to Binary (CBOR)
	data, err := cbor.Marshal(msg)
	if err != nil {
		return err
	}

	// 2. Send Bytes (Persistent) with Deduplication ID
	// Use FluxMsg.FluxID as unique Nats-Msg-Id
	// This ensures that if the LogShipper re-sends the same log (e.g., after crash/restart),
	// JetStream will identify it as a duplicate and discard it.
	// Use FluxMsg.FluxID + HopCount (len(Path)) as unique Nats-Msg-Id
	// This supports Forwarding (DAGs) AND Loops (A -> B -> A), as Path grows on every hop.
	msgID := fmt.Sprintf("%d-%d", msg.FluxID, len(msg.Path))

	_, err = n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	return err
}

// PublishRaw sends pre-serialized data (CBOR) with a specific deduplication ID.
func (n *NatsBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uint64) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// Send Bytes (Persistent) with Deduplication ID
	// Send Bytes (Persistent) with Deduplication ID
	msgID := fmt.Sprintf("%d-%s", fluxID, subject)
	_, err := n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	return err
}

// Subscribe listens for messages using a JetStream Consumer.
// Basic Bus uses Ephemeral Ordered Consumer to mimic simple sub behavior.
func (n *NatsBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	ctx := context.Background()

	// 1. Create Ephemeral Pull Consumer (Standard Worker Pattern)
	// We use a short retry loop to handle propagation latency during promotion.
	slog.Info("NATS Subscribe Initiated", "stream", n.streamName, "subject", subject)

	var cons jetstream.Consumer
	var err error
	for i := 0; i < n.retryAttempts; i++ {
		cons, err = n.js.CreateOrUpdateConsumer(ctx, n.streamName, jetstream.ConsumerConfig{
			FilterSubject: subject,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy, // Reliable baseline
			MaxAckPending: 10000,                       // Prevent stalls during high-TPS
		})
		if err == nil {
			break
		}
		time.Sleep(n.retryWait)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create ephemeral consumer for %s: %w", subject, err)
	}

	// 2. Consume Messages
	cancelCtx, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		slog.Debug("NATS Bus Delivery", "subject", subject, "len", len(msg.Data()))
		_ = msg.Ack()
		var fluxMsg fluxmsg.FluxMsg
		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			slog.Error("NATS Unmarshal Failed", "subject", subject, "error", errUnmarshal, "len", len(msg.Data()))
			return
		}
		handler(context.Background(), &fluxMsg)
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:     cc,
		cancel: cancel,
		ctx:    cancelCtx,
	}, nil
}

// SubscribeRaw listens for raw messages using a JetStream Consumer bound to a specific stream.
func (n *NatsBus) SubscribeRaw(subject string, streamName string, handler RawHandler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	ctx := context.Background()

	// 1. Create Ephemeral Pull Consumer (Standard Worker Pattern)
	cons, err := n.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ephemeral consumer (raw): %w", err)
	}

	// 2. Consume Messages
	cancelCtx, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		_ = msg.Ack()
		handler(context.Background(), msg.Subject(), msg.Data())
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:     cc,
		cancel: cancel,
		ctx:    cancelCtx,
	}, nil
}

// SubscribeDurable listens for messages using a persistent consumer (durableName).
func (n *NatsBus) SubscribeDurable(subject, durableName string, handler Handler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	ctx := context.Background()

	// 1. Create Durable Consumer
	// DeliverAllPolicy ensures full stream delivery (or resume).
	// MaxAckPending needs to be reasonable for flow control.
	cons, err := n.js.CreateOrUpdateConsumer(ctx, n.streamName, jetstream.ConsumerConfig{
		Durable:       durableName,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, err
	}

	// 2. Consume Messages
	cancelCtx, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		// Deserialize
		var fluxMsg fluxmsg.FluxMsg
		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			// If corrupt, we still Ack to move past it?
			// Ideally dead letter queue, but for now Ack + Log error (if logger avail)
			_ = msg.Ack()
			return
		}

		handler(context.Background(), &fluxMsg)
		_ = msg.Ack()
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:     cc,
		cancel: cancel,
		ctx:    cancelCtx,
	}, nil
}

func (n *NatsBus) Core() any {
	return n.conn
}

func (n *NatsBus) Close() {
	if n.conn != nil {
		// Drain ensures all buffered messages are sent before closing.
		_ = n.conn.Drain()
		n.conn.Close()
	}
}

// natsSubscription implements the Subscription interface (Wrapper)
type natsSubscription struct {
	cc     jetstream.ConsumeContext
	cancel context.CancelFunc
	ctx    context.Context
}

func (s *natsSubscription) Unsubscribe() error {
	s.cc.Stop()
	s.cancel()
	return nil
}

// KV returns the KeyValue interface for distributed state management.
func (n *NatsBus) KV() KeyValue {
	return &natsKV{
		js: n.js,
	}
}
