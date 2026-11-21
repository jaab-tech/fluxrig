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
	"github.com/fxamacker/cbor/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// RouterWrapper wraps the Watermill router to provide fluxrig-specific functionality.
// It abstracts the underlying NATS JetStream implementation.
type RouterWrapper struct {
	Router *message.Router
	Pub    message.Publisher
	Sub    message.Subscriber

	// NC is a plain, persistent NATS connection, separate from the one
	// Watermill's Publisher/Subscriber hold internally (which is not
	// reachable from here). It exists for callers that need the bus directly
	// rather than through a Watermill wire, such as the control plane's
	// request/reply confirmation. Nil until ConfigureJetStream succeeds.
	NC *nats.Conn
}

// NewRouter creates a standard Watermill Router.
func NewRouter(logger watermill.LoggerAdapter) (*RouterWrapper, error) {
	r, err := message.NewRouter(message.RouterConfig{}, logger)
	if err != nil {
		return nil, err
	}
	return &RouterWrapper{Router: r}, nil
}

// JetStreamSetup describes the streams and the connection ConfigureJetStream
// works with. It is a struct rather than a parameter list because the list had
// grown to eleven, with adjacent strings and bools that were easy to swap
// silently.
type JetStreamSetup struct {
	// URL is the NATS URL, for example "nats://localhost:4222". Domain is the
	// JetStream domain, and RootCA the authority for a TLS connection.
	URL     string
	Domain  string
	RootCA  string
	Durable bool

	// The two streams this router provisions, with their subjects and their
	// max age. An empty max age means no limit.
	BusinessStream    string
	BusinessSubjects  []string
	BusinessMaxAge    string
	TelemetryStream   string
	TelemetrySubjects []string
	TelemetryMaxAge   string

	// Replicas is how many copies of each stream to keep. Zero or one is a
	// single copy; more needs a cluster of at least that many members.
	Replicas int

	// QueueGroup, when set, makes this Mixer's subscriptions a shared durable
	// queue group: exactly one member handles each message. Empty keeps every
	// Mixer subscribing on its own, which is right for a single Mixer and for
	// topics every Mixer must see.
	QueueGroup string

	// AckWait is how long the queue group's consumer waits for a message's
	// acknowledgement before it delivers the message again, to another member
	// or the same one: a message pushed to a Mixer that was cut off comes
	// back after it (snake.ack_wait). Zero keeps the server default.
	AckWait time.Duration

	// FormTimeout bounds how long stream creation keeps retrying while a
	// cluster forms. Zero takes defaultStreamFormTimeout.
	FormTimeout time.Duration

	// ClientOptions secures every connection of the router, when NATS is
	// external. The embedded server takes RootCA alone.
	ClientOptions []nats.Option
}

// defaultStreamFormTimeout matches the Snake's own default form timeout
// (snake.form_timeout).
const defaultStreamFormTimeout = 2 * time.Minute

// formRetryWait is the pause between two attempts to create a stream or a
// subscription while the cluster forms.
const formRetryWait = time.Second

// ConfigureJetStream sets up the NATS JetStream Publisher and Subscriber, and
// provisions the two streams they use.
func (r *RouterWrapper) ConfigureJetStream(setup JetStreamSetup, logger watermill.LoggerAdapter) error {
	url, domain, rootCA, durable := setup.URL, setup.Domain, setup.RootCA, setup.Durable
	businessStream, businessSubjects := setup.BusinessStream, setup.BusinessSubjects
	telemetryStream, telemetrySubjects := setup.TelemetryStream, setup.TelemetrySubjects
	businessMaxAgeStr, telemetryMaxAgeStr := setup.BusinessMaxAge, setup.TelemetryMaxAge
	replicas := setup.Replicas
	if replicas < 1 {
		replicas = 1
	}
	// 1. Manually Ensure Stream Exists using V2 SDK
	connOpts := []nats.Option{}
	if rootCA != "" {
		connOpts = append(connOpts, nats.RootCAs(rootCA))
	}

	connOpts = append(connOpts, setup.ClientOptions...)
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

	// The context bounds the whole retry window below. It was a fixed 10s,
	// which the first 10s attempt used up, so the retry never ran.
	formTimeout := setup.FormTimeout
	if formTimeout <= 0 {
		formTimeout = defaultStreamFormTimeout
	}
	streamDeadline := time.Now().Add(formTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), streamDeadline)
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

	// 1. Determine Streams to Create/Update
	streams := make(map[string][]string)
	maxAges := make(map[string]time.Duration)

	if businessStream != "" {
		streams[businessStream] = businessSubjects
		maxAges[businessStream] = businessMaxAge
	}

	if telemetryStream != "" {
		if subjects, exists := streams[telemetryStream]; exists {
			// Merge subjects if names match
			seen := make(map[string]bool)
			for _, s := range subjects {
				seen[s] = true
			}
			for _, s := range telemetrySubjects {
				if !seen[s] {
					streams[telemetryStream] = append(streams[telemetryStream], s)
					seen[s] = true
				}
			}
			if businessMaxAge > telemetryMaxAge {
				maxAges[telemetryStream] = businessMaxAge
			}
		} else {
			streams[telemetryStream] = telemetrySubjects
			maxAges[telemetryStream] = telemetryMaxAge
		}
	}

	// 2. Create/Update Streams
	//
	// Placing a stream's replicas needs one member per replica, and on a fresh
	// cluster the last member may still be starting, so a single short attempt
	// fails where a retry succeeds. The window matches the Snake's stream
	// provisioning, which already retries for the same reason. Without this, a
	// three-Mixer cluster starting at once died on "failed to create/update
	// stream flux-telemetry: context deadline exceeded", which the phase-6
	// testbed caught.
	const streamAttemptTimeout = 10 * time.Second
	for name, subjects := range streams {
		var lastErr error
		for attempt := 1; ; attempt++ {
			logger.Info("Configuring JetStream Stream", watermill.LogFields{
				"stream":   name,
				"subjects": subjects,
				"attempt":  attempt,
			})
			actx, cancel := context.WithTimeout(ctx, streamAttemptTimeout)
			_, lastErr = js.CreateOrUpdateStream(actx, jetstream.StreamConfig{
				Name:      name,
				Subjects:  subjects,
				Retention: jetstream.LimitsPolicy,
				Storage:   jetstream.FileStorage,
				MaxAge:    maxAges[name],
				Replicas:  replicas,
			})
			cancel()
			if lastErr == nil {
				break
			}
			if time.Now().After(streamDeadline) {
				return fmt.Errorf("failed to create/update stream %s: %w", name, lastErr)
			}
			logger.Info("Stream creation is waiting for the cluster", watermill.LogFields{
				"stream": name, "attempt": attempt, "error": lastErr,
			})
			select {
			case <-ctx.Done():
				return fmt.Errorf("failed to create/update stream %s: %w", name, ctx.Err())
			case <-time.After(formRetryWait):
			}
		}
	}

	// Log confirmation
	logger.Info("JetStream Streams Verified (V2 SDK)", watermill.LogFields{
		"business_stream":  businessStream,
		"telemetry_stream": telemetryStream,
		"domain":           domain,
	})

	// 2. Configure Watermill
	natsOpts := []nats.Option{
		nats.Name("fluxrig Router"),
		nats.Timeout(10 * time.Second),
		nats.ReconnectWait(1 * time.Second),
		nats.MaxReconnects(-1),
	}
	if rootCA != "" {
		natsOpts = append(natsOpts, nats.RootCAs(rootCA))
	}
	natsOpts = append(natsOpts, setup.ClientOptions...)

	subscribeOpts := []nats.SubOpt{}
	// DeliverLastPerSubject delivers only the latest message per subject and
	// then stops. It is right for a single-process reader that wants current
	// state, but it cannot back a SHARED durable queue group across a cluster:
	// one durable name over several subjects on one stream is rejected with
	// "subject does not match consumer", which the phase-6 testbed caught.
	// So a queue-grouped subscriber uses a plain ack-based durable instead.
	if setup.QueueGroup == "" {
		if durable {
			logger.Info("NATS Consumer Mode: DURABLE (DeliverLastPerSubject)", nil)
		} else {
			logger.Info("NATS Consumer Mode: EPHEMERAL (DeliverLastPerSubject)", nil)
		}
		subscribeOpts = append(subscribeOpts, nats.DeliverLastPerSubject())
	}

	jsConfig := watermillNats.JetStreamConfig{
		Disabled:         false,
		AutoProvision:    false, // Handled manually above
		ConnectOptions:   []nats.JSOpt{},
		SubscribeOptions: subscribeOpts,
		PublishOptions:   []nats.PubOpt{},
		AckAsync:         false,
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

	if setup.QueueGroup != "" {
		// A shared durable queue group: one member handles each message, and
		// the consumer survives a member leaving. The durable name must be
		// UNIQUE PER TOPIC: one durable name over two subjects on one stream
		// makes the second subscribe fail with "subject does not match
		// consumer", which the phase-6 testbed caught. Derive it from the
		// prefix plus the topic, sanitized to a valid NATS token. This must be
		// set on jsConfig BEFORE it is copied into subCfg below.
		jsConfig.DurablePrefix = setup.QueueGroup
		jsConfig.DurableCalculator = queueDurableName
	}

	// Subscriber (Reads from NATS Consumer -> Watermill handlers)
	subCfg := watermillNats.SubscriberConfig{
		URL:         url,
		Unmarshaler: RawNATSMarshaler{}, // Passthrough for FluxMsg (CBOR)
		JetStream:   jsConfig,
		NatsOptions: natsOpts,
	}
	if setup.QueueGroup != "" {
		subCfg.QueueGroupPrefix = setup.QueueGroup
	}
	sub, err := watermillNats.NewSubscriber(subCfg, logger)
	if err != nil {
		return err
	}

	// A persistent connection for callers that need the bus directly, such as
	// the control plane's request/reply confirmation: separate from the
	// provisioning connection above (closed by the defer) and from whatever
	// Watermill's Publisher/Subscriber hold internally (not reachable here).
	ncPersistent, err := nats.Connect(url, natsOpts...)
	if err != nil {
		return fmt.Errorf("failed to open the persistent NATS connection: %w", err)
	}

	r.Pub = pub
	r.Sub = sub
	if setup.QueueGroup != "" {
		// The shared consumers are created here, not by nats.go, so no
		// member's shutdown deletes them (see queueConsumerSubscriber).
		r.Sub = &queueConsumerSubscriber{
			Subscriber: sub, nc: ncPersistent, group: setup.QueueGroup, timeout: streamAttemptTimeout,
			formTimeout: formTimeout, retryWait: formRetryWait, ackWait: setup.AckWait, replicas: replicas, logger: logger,
		}
	}
	r.NC = ncPersistent
	return nil
}

// Run starts the router (blocking).
func (r *RouterWrapper) Run(ctx context.Context) error {
	return r.Router.Run(ctx)
}

// Publish implements ScenarioPublisher interface (high-level fluxmsg).
func (r *RouterWrapper) Publish(ctx context.Context, subject string, fm *fluxmsg.FluxMsg) error {
	if r.Pub == nil {
		return fmt.Errorf("publisher not initialized")
	}
	payload, err := cbor.Marshal(fm)
	if err != nil {
		return err
	}
	msg := message.NewMessage(watermill.NewUUID(), payload)
	return r.Pub.Publish(subject, msg)
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
	if r.NC != nil {
		r.NC.Close()
	}
	return r.Router.Close()
}
