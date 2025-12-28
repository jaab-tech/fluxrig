package router

import (
	"context"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/nats-io/nats.go"
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

func (r *RouterWrapper) ConfigureJetStream(url string, streamName string, logger watermill.LoggerAdapter) error {
	// 1. Manually Ensure Stream Exists using Legacy Context
	nc, err := nats.Connect(url)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Use Legacy JetStream Context
	js, err := nc.JetStream()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	// Check if stream exists
	_, err = js.StreamInfo(streamName)
	if err == nil {
		// Stream exists, we trust Snake/Config provisioned it correctly
	} else {
		// Create stream
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      streamName,
			Subjects:  []string{"fluxrig.>", "flux.telemetry.>"},
			Retention: nats.LimitsPolicy,
			Storage:   nats.FileStorage,
		})
		if err != nil {
			return err
		}
	}

	// Log confirmation
	logger.Info("JetStream Stream Verified", watermill.LogFields{
		"stream": streamName,
	})

	// 2. Configure Watermill
	natsOpts := []nats.Option{
		nats.Name("FluxRig Router"),
		nats.Timeout(10 * time.Second),
		nats.ReconnectWait(1 * time.Second),
		nats.MaxReconnects(-1),
	}

	jsConfig := wnats.JetStreamConfig{
		Disabled:      false,
		AutoProvision: false, // We handled it manually above
		SubscribeOptions: []nats.SubOpt{
			nats.DeliverAll(),
			// nats.BindStream("flux"), // Let NATS resolve via subject to avoid "duplicate stream name" error
		},
		PublishOptions: []nats.PubOpt{
			// Sync publish by default for durability
		},
		AckAsync: false,
		// DurablePrefix: "", // EPHEMERAL MODE: Disabled to avoid dot-naming issues in Consumers for now.
	}

	// Publisher (Writes to Watermill wires -> NATS Streams)
	pub, err := wnats.NewPublisher(
		wnats.PublisherConfig{
			URL:         url,
			Marshaler:   RawNATSMarshaler{}, // Passthrough for FluxMsg (MsgPack)
			JetStream:   jsConfig,
			NatsOptions: natsOpts,
		},
		logger,
	)
	if err != nil {
		return err
	}

	// Subscriber (Reads from NATS Consumer -> Watermill handlers)
	sub, err := wnats.NewSubscriber(
		wnats.SubscriberConfig{
			URL:         url,
			Unmarshaler: RawNATSMarshaler{}, // Passthrough for FluxMsg (MsgPack)
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
