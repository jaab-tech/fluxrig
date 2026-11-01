// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Defaults of a reader.
const (
	DefaultMaxEventAge   = 30 * time.Second
	DefaultMaxDeliver    = 5
	DefaultInactiveAfter = time.Hour
)

const (
	// readerBatch is how many events a reader asks for at a time. It stays low:
	// the age and the ack wait of an event run while it waits on the client.
	readerBatch = 16
	// readerCheck is how often a waiting reader checks its connection.
	readerCheck = time.Second
	// readerConsumerCheck is how often a waiting reader asks whether its consumer
	// exists. The client reports a deleted consumer only when a pull is pending at
	// that moment, so an idle reader could otherwise wait for nothing, for example
	// after a repair recreates the stream.
	readerConsumerCheck = 10 * time.Second
	// redeliverDelay spaces the retries of a failed event, so a short failure
	// does not spend all of max_deliver at once.
	redeliverDelay = time.Second
)

// ErrNotConnected reports a reader with no usable connection. Open it again
// later: the durable consumer keeps its place.
var ErrNotConnected = errors.New("events: not connected")

// ReaderConfig configures a Reader.
type ReaderConfig struct {
	// Conn returns the current connection, or nil when there is none.
	Conn func() *nats.Conn
	// Subject is the subject to read, from ConnSubject.
	Subject string
	// Durable is the consumer name, from DurableName.
	Durable string
	// On lists the events to read. Empty reads every event.
	On []string
	// MaxEventAge drops an event older than this, by its ts.
	MaxEventAge time.Duration
	// MaxDeliver bounds the deliveries of an event that fails.
	MaxDeliver int
	// InactiveAfter deletes a consumer with no reader for this long.
	InactiveAfter time.Duration
	// Logger is the reader's logger.
	Logger *slog.Logger
}

// Delivery is one event for the reader. The caller must call Ack once.
type Delivery struct {
	Event ConnEvent
	// Data is the event as it was published.
	Data []byte

	msg jetstream.Msg
	r   *Reader
}

// Ack settles the event when the work on it ends. A nil err removes it. Any
// other err delivers it again, up to MaxDeliver.
func (d *Delivery) Ack(err error) error {
	if err == nil {
		return d.msg.Ack()
	}
	if meta, errMeta := d.msg.Metadata(); errMeta == nil && meta.NumDelivered >= uint64(d.r.cfg.MaxDeliver) { //nolint:gosec // a small configured count
		d.r.cfg.Logger.Warn("event given up after its last delivery",
			"event", d.Event.Event, "conn_id", d.Event.ConnID, "deliveries", meta.NumDelivered, "error", err)
	}
	return d.msg.NakWithDelay(redeliverDelay)
}

// Reader reads events with a durable consumer, which outlives it.
type Reader struct {
	cfg ReaderConfig

	mu      sync.Mutex
	conn    *nats.Conn
	cons    jetstream.Consumer
	cc      jetstream.ConsumeContext
	msgs    chan jetstream.Msg
	stopped chan struct{}
	broken  chan struct{}
}

// NewReader creates a Reader and applies the defaults. It does not connect.
func NewReader(cfg ReaderConfig) *Reader {
	if cfg.MaxEventAge <= 0 {
		cfg.MaxEventAge = DefaultMaxEventAge
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = DefaultMaxDeliver
	}
	if cfg.InactiveAfter <= 0 {
		cfg.InactiveAfter = DefaultInactiveAfter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Reader{cfg: cfg}
}

// Open creates or finds the durable consumer and starts to read. It returns
// ErrNotConnected while there is no connection.
//
// A new consumer starts at the beginning of the stream. An event published
// before the reader opened is then not lost, and what is too old is dropped by
// its age.
func (r *Reader) Open(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cc != nil {
		return nil
	}

	nc := r.cfg.Conn()
	if nc == nil || !nc.IsConnected() {
		return ErrNotConnected
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("events: open jetstream: %w", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       r.cfg.Durable,
		FilterSubject: r.cfg.Subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		// An event not settled inside its age is too old to deliver again.
		AckWait:           r.cfg.MaxEventAge,
		MaxDeliver:        r.cfg.MaxDeliver,
		InactiveThreshold: r.cfg.InactiveAfter,
	})
	if err != nil {
		return fmt.Errorf("events: consumer %s: %w", r.cfg.Durable, err)
	}

	msgs := make(chan jetstream.Msg)
	stopped := make(chan struct{})
	broken := make(chan struct{})
	var once sync.Once
	cc, err := cons.Consume(func(m jetstream.Msg) {
		select {
		case msgs <- m:
		case <-stopped:
		}
	}, jetstream.PullMaxMessages(readerBatch), jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		if errors.Is(err, jetstream.ErrConsumerDeleted) {
			once.Do(func() { close(broken) })
		}
		r.cfg.Logger.Debug("event reader", "durable", r.cfg.Durable, "error", err)
	}))
	if err != nil {
		return fmt.Errorf("events: consume %s: %w", r.cfg.Durable, err)
	}
	r.conn, r.cons, r.cc, r.msgs, r.stopped, r.broken = nc, cons, cc, msgs, stopped, broken
	return nil
}

// Next returns the next event for this reader. It settles what it does not
// return: an event that does not decode, an event the reader did not ask for,
// and an event that is too old. It returns ErrNotConnected when the reader must
// be opened again.
func (r *Reader) Next(ctx context.Context) (*Delivery, error) {
	r.mu.Lock()
	msgs, broken := r.msgs, r.broken
	r.mu.Unlock()
	if msgs == nil {
		return nil, ErrNotConnected
	}

	ticker := time.NewTicker(readerCheck)
	defer ticker.Stop()
	asked := time.Now()
	for {
		select {
		case m := <-msgs:
			if d := r.accept(m); d != nil {
				return d, nil
			}
		case <-broken:
			r.Close()
			return nil, ErrNotConnected
		case <-ticker.C:
			lost := r.connectionLost()
			if !lost && time.Since(asked) >= readerConsumerCheck {
				asked = time.Now()
				lost = r.consumerGone(ctx)
			}
			if lost {
				r.Close()
				return nil, ErrNotConnected
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// connectionLost reports a connection that will not come back, or that the Rack
// replaced. A connection that is reconnecting comes back by itself.
func (r *Reader) connectionLost() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn == nil || r.conn.IsClosed() || r.conn != r.cfg.Conn()
}

// consumerGone reports a consumer or a stream that the server no longer has. Any
// other answer, or none, says nothing about it.
func (r *Reader) consumerGone(ctx context.Context) bool {
	r.mu.Lock()
	cons := r.cons
	r.mu.Unlock()
	if cons == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, readerConsumerCheck)
	defer cancel()
	_, err := cons.Info(ctx)
	return errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound)
}

func (r *Reader) accept(m jetstream.Msg) *Delivery {
	var ev ConnEvent
	if err := json.Unmarshal(m.Data(), &ev); err != nil {
		r.cfg.Logger.Warn("event dropped, it does not decode", "durable", r.cfg.Durable, "error", err)
		_ = m.Term()
		return nil
	}
	if len(r.cfg.On) > 0 && !slices.Contains(r.cfg.On, ev.Event) {
		_ = m.Ack()
		return nil
	}
	if age := time.Since(ev.TS); age > r.cfg.MaxEventAge {
		r.cfg.Logger.Info("event dropped, it is too old",
			"event", ev.Event, "conn_id", ev.ConnID, "age", age.Round(time.Millisecond), "max_event_age", r.cfg.MaxEventAge)
		_ = m.Ack()
		return nil
	}
	return &Delivery{Event: ev, Data: m.Data(), msg: m, r: r}
}

// Close stops reading and keeps the durable consumer. It is safe to call twice.
func (r *Reader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cc == nil {
		return
	}
	close(r.stopped)
	r.cc.Stop()
	r.conn, r.cons, r.cc, r.msgs, r.stopped, r.broken = nil, nil, nil, nil, nil, nil
}
