package bus

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/vmihailenco/msgpack/v5"
)

// NatsBus is the concrete implementation of the Bus interface using NATS JetStream.
type NatsBus struct {
	conn       *nats.Conn
	js         jetstream.JetStream
	streamName string
}

// NewNatsBus creates a new instance.
func NewNatsBus(streamName string) *NatsBus {
	return &NatsBus{
		streamName: streamName,
	}
}

// Connect establishes the connection to the NATS server and initializes JetStream.
func (n *NatsBus) Connect(url string, name string, connectTimeout time.Duration, reconnectWait time.Duration) error {
	// 1. Connect to NATS Core
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.Timeout(connectTimeout),
		nats.ReconnectWait(reconnectWait),
		nats.MaxReconnects(-1), // Infinite reconnects
	)
	if err != nil {
		return err
	}
	n.conn = nc

	// 2. Initialize JetStream
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	n.js = js

	return nil
}

// Publish serializes the FluxMsg to MsgPack bytes and sends it via JetStream.
func (n *NatsBus) Publish(subject string, msg *fluxmsg.FluxMsg) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}

	// 1. Serialize to Binary (MsgPack)
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return err
	}

	// 2. Send Bytes (Persistent) with Deduplication ID
	// Use FluxMsg.FluxID as unique Nats-Msg-Id
	// This ensures that if the LogShipper re-sends the same log (e.g., after crash/restart),
	// JetStream will identify it as a duplicate and discard it.
	msgID := strconv.FormatUint(msg.FluxID, 10)

	_, err = n.js.Publish(context.Background(), subject, data, jetstream.WithMsgID(msgID))
	return err
}

// PublishWithContext sends with specific context (useful for shutdowns).
func (n *NatsBus) PublishWithContext(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	if n.js == nil {
		return errors.New("nats bus not connected")
	}
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return err
	}

	msgID := strconv.FormatUint(msg.FluxID, 10)

	_, err = n.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	return err
}

// Subscribe listens for messages using a JetStream Consumer.
// Basic Bus uses Ephemeral Ordered Consumer to mimic simple sub behavior.
func (n *NatsBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	if n.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	ctx := context.Background()

	// 1. Create Ordered Consumer (Ephemeral, In-Memory for speed)
	// This gives us "Simple Subscribe" semantics but backed by the Stream.
	cons, err := n.js.CreateOrUpdateConsumer(ctx, n.streamName, jetstream.ConsumerConfig{
		Name:          "", // Ephemeral
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverNewPolicy, // Only new messages
	})
	if err != nil {
		return nil, err
	}

	// 2. Consume Messages
	cancelCtx, cancel := context.WithCancel(ctx)

	// We start a goroutine to consume
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		// Auto-Ack for simple subscription
		_ = msg.Ack()

		// Deserialize
		var fluxMsg fluxmsg.FluxMsg
		if err := msgpack.Unmarshal(msg.Data(), &fluxMsg); err != nil {
			return // Drop corrupt
		}

		handler(&fluxMsg)
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
		if err := msgpack.Unmarshal(msg.Data(), &fluxMsg); err != nil {
			// If corrupt, we still Ack to move past it?
			// Ideally dead letter queue, but for now Ack + Log error (if logger avail)
			_ = msg.Ack()
			return
		}

		handler(&fluxMsg)
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

func (n *NatsBus) Close() {
	if n.conn != nil {
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
