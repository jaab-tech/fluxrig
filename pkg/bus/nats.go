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
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// NatsBus is the concrete implementation of the Bus interface using NATS JetStream.
type NatsBus struct {
	conn              *nats.Conn
	js                jetstream.JetStream
	streamName        string
	retryWait         time.Duration
	retryAttempts     int
	inactiveThreshold time.Duration
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

	// --- In-Process Optimization ---
	if opts.InProcessServer != nil {
		url = "nats://localhost:4222" // Dummy URL required by NATS client even for in-process
		// Use special InProcess provider if we can cast it
		if srv, ok := opts.InProcessServer.(interface {
			InProcessConn(opts ...nats.Option) (*nats.Conn, error)
		}); ok {
			slog.Info("Establishing In-Process NATS connection (Bypassing network stack)")
			nc, err := srv.InProcessConn(natsOpts...)
			if err != nil {
				return err
			}
			n.conn = nc
			return n.finalizeConnect(opts)
		}
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

	return n.finalizeConnect(opts)
}

func (n *NatsBus) finalizeConnect(opts ConnectOptions) error {
	n.retryWait = opts.SubscriptionRetryWait
	n.retryAttempts = opts.SubscriptionRetryAttempts
	if n.retryAttempts <= 0 {
		n.retryAttempts = 1 // At least one attempt
	}
	n.inactiveThreshold = opts.InactiveThreshold

	// 3. Initialize JetStream
	js, err := jetstream.New(n.conn)
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

	// 3. Technical Logging (Avoid recursion for telemetry)
	if !strings.Contains(subject, "telemetry") && !strings.Contains(subject, "logs") && !strings.Contains(subject, "metrics") {
		slog.Debug("NATS Bus: Publish",
			"subject", subject,
			"flux_id", msg.FluxID.String(),
		)
	}

	// 2. Send Bytes (Persistent) with Deduplication ID
	msgID := fmt.Sprintf("%s-%d", msg.FluxID.String(), len(msg.Path))

	// 3. Inject Traces (OTel)
	if ctx != nil {
		otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(msg.Metadata))
	}

	_, err = n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	if err != nil {
		slog.Error("NATS Bus Publish Failed", "subject", subject, "error", err)
	}
	return err
}

// Purge removes all messages from the stream (Best Effort)
func (n *NatsBus) Purge(ctx context.Context) error {
	if n.js == nil || n.streamName == "" {
		return nil
	}
	s, err := n.js.Stream(ctx, n.streamName)
	if err != nil {
		return nil // Stream might not exist yet
	}
	return s.Purge(ctx)
}

// PublishRaw sends pre-serialized data (CBOR) with a specific deduplication ID.
func (n *NatsBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// Send Bytes (Persistent) with Deduplication ID
	msgID := fmt.Sprintf("%s-%s", fluxID.String(), subject)
	_, err := n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	if err != nil {
		slog.Error("NATS Bus PublishRaw Failed", "subject", subject, "error", err)
	}
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
	var consName string
	for i := 0; i < n.retryAttempts; i++ {
		consName = fmt.Sprintf("flux-sub-%s", uuid.New().String())
		cons, err = n.js.CreateOrUpdateConsumer(ctx, n.streamName, jetstream.ConsumerConfig{
			Name:              consName,
			FilterSubject:     subject,
			DeliverPolicy:     jetstream.DeliverNewPolicy,
			AckPolicy:         jetstream.AckExplicitPolicy, // Reliable baseline
			MaxAckPending:     10000,                       // Prevent stalls during high-TPS
			InactiveThreshold: n.inactiveThreshold,         // Autonomous server-side cleanup if client crashes
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
	_, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		_ = msg.Ack()
		var fluxMsg fluxmsg.FluxMsg

		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			return
		}

		// 2. Extract Traces (OTel)
		ctx := context.Background()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(fluxMsg.Metadata))

		handler(ctx, &fluxMsg)
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:         cc,
		cancel:     cancel,
		js:         n.js,
		streamName: n.streamName,
		consName:   consName,
		isDurable:  false,
	}, nil
}

// SubscribeRaw listens for raw messages using a JetStream Consumer bound to a specific stream.
func (n *NatsBus) SubscribeRaw(subject string, streamName string, handler RawHandler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	ctx := context.Background()

	// 1. Create Ephemeral Pull Consumer
	consName := fmt.Sprintf("flux-raw-%s", uuid.New().String())
	cons, err := n.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:              consName,
		FilterSubject:     subject,
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: n.inactiveThreshold, // Autonomous server-side cleanup
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ephemeral consumer (raw): %w", err)
	}

	// 2. Consume Messages
	_, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		_ = msg.Ack()
		handler(context.Background(), msg.Subject(), msg.Data())
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:         cc,
		cancel:     cancel,
		js:         n.js,
		streamName: streamName,
		consName:   consName,
		isDurable:  false,
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
	_, cancel := context.WithCancel(ctx)

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		// Deserialize
		var fluxMsg fluxmsg.FluxMsg
		if errUnmarshal := cbor.Unmarshal(msg.Data(), &fluxMsg); errUnmarshal != nil {
			// If corrupt, we still Ack to move past it?
			// Ideally dead letter queue, but for now Ack + Log error (if logger avail)
			_ = msg.Ack()
			return
		}

		// Extract Traces (OTel)
		ctx := context.Background()
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(fluxMsg.Metadata))

		handler(ctx, &fluxMsg)
		_ = msg.Ack()
	})

	if err != nil {
		cancel()
		return nil, err
	}

	return &natsSubscription{
		cc:         cc,
		cancel:     cancel,
		js:         n.js,
		streamName: n.streamName,
		consName:   cons.CachedInfo().Name,
		isDurable:  true,
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
	cc         jetstream.ConsumeContext
	cancel     context.CancelFunc
	js         jetstream.JetStream
	streamName string
	consName   string
	isDurable  bool
}

func (s *natsSubscription) Unsubscribe() error {
	s.cc.Stop()
	s.cancel()

	// Best effort: delete the ephemeral consumer to avoid overlap with new subscriptions
	if !s.isDurable && s.js != nil && s.streamName != "" && s.consName != "" {
		slog.Debug("NATS Bus Unsubscribe: Deleting ephemeral consumer", "stream", s.streamName, "consumer", s.consName)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.js.DeleteConsumer(ctx, s.streamName, s.consName); err != nil {
			// Log as warning rather than error as it might be already gone (race condition with InactiveThreshold)
			slog.Warn("NATS Bus Unsubscribe: Failed to delete ephemeral consumer", "stream", s.streamName, "consumer", s.consName, "error", err)
		}
	}
	return nil
}

// KV returns the KeyValue interface for distributed state management.
func (n *NatsBus) KV() KeyValue {
	return &natsKV{
		js: n.js,
	}
}
