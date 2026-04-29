// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermillNats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// RouterWrapper wraps the Watermill router to provide fluxrig-specific functionality.
// It abstracts the underlying NATS JetStream implementation.
type RouterWrapper struct {
	Router *message.Router
	Pub    message.Publisher
	Sub    message.Subscriber
}

// NewRouter creates a standard Watermill Router.
func NewRouter(logger watermill.LoggerAdapter) (*RouterWrapper, error) {
	r, err := message.NewRouter(message.RouterConfig{}, logger)
	if err != nil {
		return nil, err
	}
	return &RouterWrapper{Router: r}, nil
}

// ConfigureJetStream sets up NATS JetStream Publisher and Subscriber.
// url: NATS URL (e.g. "nats://localhost:4222")

func (r *RouterWrapper) ConfigureJetStream(url string, domain string, durable bool, rootCA string, businessMaxAgeStr string, telemetryMaxAgeStr string, logger watermill.LoggerAdapter) error {
	// 1. Manually Ensure Stream Exists using V2 SDK
	connOpts := []nats.Option{}
	if rootCA != "" {
		connOpts = append(connOpts, nats.RootCAs(rootCA))
	}

	nc, err := nats.Connect(url, connOpts...)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Initialize V2 JS Context
	// Force Default Domain for local/ephemeral discovery reliability.
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("failed to initialize jetstream: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Parse custom max age configuration
	businessMaxAge, err := time.ParseDuration(businessMaxAgeStr)
	if err != nil || businessMaxAge <= 0 {
		businessMaxAge = 24 * 30 * time.Hour // Fallback default
	}

	telemetryMaxAge, err := time.ParseDuration(telemetryMaxAgeStr)
	if err != nil || telemetryMaxAge <= 0 {
		telemetryMaxAge = 24 * time.Hour // Fallback default
	}

	// Check if stream exists
	// A. Business Stream (WorkQueue or Limits, Durable)
	businessStream := "flux-msg"
	businessSubjects := []string{"fluxrig.>", "flux.msg.>"}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      businessStream,
		Subjects:  businessSubjects,
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    businessMaxAge,
		Replicas:  1,
	})
	if err != nil {
		return fmt.Errorf("failed to create/update business stream: %w", err)
	}

	// B. Telemetry Stream (Limits, Short-lived)
	telemetryStream := "flux-telemetry"
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      telemetryStream,
		Subjects:  []string{"flux.telemetry.>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    telemetryMaxAge,
		MaxMsgs:   100000, // Cap number of logs
		Replicas:  1,
	})
	if err != nil {
		return fmt.Errorf("failed to create/update telemetry stream: %w", err)
	}

	// Log confirmation
	logger.Info("JetStream Streams Verified (V2 SDK)", watermill.LogFields{
		"business_stream":  businessStream,
		"telemetry_stream": telemetryStream,
		"domain":           domain,
	})

	// 2. Configure Watermill
	natsOpts := []nats.Option{
		nats.Name("FluxRig Router"),
		nats.Timeout(10 * time.Second),
		nats.ReconnectWait(1 * time.Second),
		nats.MaxReconnects(-1),
	}
	if rootCA != "" {
		natsOpts = append(natsOpts, nats.RootCAs(rootCA))
	}

	subscribeOpts := []nats.SubOpt{}
	if durable {
		// [PROD] Durable Mode: Resumes from last acked message. Zero data loss.
		logger.Info("NATS Consumer Mode: DURABLE (DeliverAll)", nil)
		subscribeOpts = append(subscribeOpts,
			nats.DeliverAll(),
			nats.Durable("flux-router"), // Shared Durable Name
		)
	} else {
		// [DEV] Ephemeral Mode: Catch up from beginning of stream to avoid race conditions
		logger.Info("NATS Consumer Mode: EPHEMERAL (DeliverAll/Catchup)", nil)
		subscribeOpts = append(subscribeOpts,
			nats.DeliverAll(),
		)
	}

	jsConfig := watermillNats.JetStreamConfig{
		Disabled:       false,
		AutoProvision:  false, // Handled manually above
		ConnectOptions: []nats.JSOpt{
			// Enforce default domain mapping
		},
		SubscribeOptions: subscribeOpts,
		PublishOptions:   []nats.PubOpt{
			// Sync publish by default for durability
		},
		AckAsync: false,
	}

	// Publisher (Writes to Watermill wires -> NATS Streams)
	pub, err := watermillNats.NewPublisher(
		watermillNats.PublisherConfig{
			URL:         url,
			Marshaler:   RawNATSMarshaler{}, // Passthrough for FluxMsg (CBOR)
			JetStream:   jsConfig,
			NatsOptions: natsOpts,
		},
		logger,
	)
	if err != nil {
		return err
	}

	// Subscriber (Reads from NATS Consumer -> Watermill handlers)
	sub, err := watermillNats.NewSubscriber(
		watermillNats.SubscriberConfig{
			URL:         url,
			Unmarshaler: RawNATSMarshaler{}, // Passthrough for FluxMsg (CBOR)
			JetStream:   jsConfig,
			// QueueGroupPrefix: "flux_consumer", // Disabled for Ephemeral Mode
			NatsOptions: natsOpts,
		},
		logger,
	)
	if err != nil {
		return err
	}

	r.Pub = pub
	r.Sub = sub
	return nil
}

// Run starts the router (blocking).
func (r *RouterWrapper) Run(ctx context.Context) error {
	return r.Router.Run(ctx)
}

// RawNATSMarshaler passes raw bytes through without Watermill envelope
type RawNATSMarshaler struct{}

func (m RawNATSMarshaler) Marshal(topic string, msg *message.Message) (*nats.Msg, error) {
	return &nats.Msg{
		Subject: topic,
		Data:    msg.Payload,
	}, nil
}

func (m RawNATSMarshaler) Unmarshal(msg *nats.Msg) (*message.Message, error) {
	wmMsg := message.NewMessage(watermill.NewUUID(), msg.Data)
	return wmMsg, nil
}

// Close cleans up connections.
func (r *RouterWrapper) Close() error {
	if r.Pub != nil {
		if err := r.Pub.Close(); err != nil {
			return err
		}
	}
	if r.Sub != nil {
		if err := r.Sub.Close(); err != nil {
			return err
		}
	}
	return r.Router.Close()
}
