// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// Fallbacks for a bus connected without these options: the defaults of
// snake.subscription_retry_wait and snake.operation_timeout.
const (
	defaultSubscriptionRetryWait = 200 * time.Millisecond
	defaultOperationTimeout      = 5 * time.Second
)

// liveConsumer keeps the ephemeral consumer of one subscription alive.
//
// An ephemeral consumer has one replica, on one server of the cluster. When
// that server stops, the consumer stops with it, and the client keeps asking
// it for messages without an error that stops it: the subscription stalls in
// silence. liveConsumer watches for that, creates a new consumer, and resumes
// it at the first stream sequence the handler has not settled, so a message
// published while the consumer was gone is delivered, not skipped. A message
// can arrive twice across the change; the guaranteed lane already allows it.
type liveConsumer struct {
	js jetstream.JetStream
	// note reports each JetStream request's outcome to the bus, so a server
	// that answers none of them moves the connection (snake.failover_after).
	note   func(subject string, err error)
	stream string
	prefix string // consumer name prefix
	base   jetstream.ConsumerConfig
	// replicas, when set, gives the replica count of each consumer at the
	// moment it is created, the first one and every replacement. An error
	// fails that creation, which is retried: a consumer created without a
	// count (0) is a single replica for its whole life.
	replicas func(ctx context.Context) (int, error)
	// maxMissed is how many heartbeats in a row the consumer may miss before
	// it is replaced (snake.consumer_missed_heartbeats); 0 takes the default.
	maxMissed int
	// keep leaves the consumer to its InactiveThreshold instead of deleting it
	// (ConnectOptions.KeepConsumers).
	keep bool
	// busClosed, when set, ends the retries of start: the bus is closing.
	busClosed <-chan struct{}
	retryWait time.Duration
	opTimeout time.Duration
	// handle processes one message and reports whether it is settled (acked
	// or terminated). An unsettled message is redelivered by the consumer,
	// and by a replacement consumer if this one is lost.
	handle func(jetstream.Msg) bool

	started time.Time
	// replay starts from the last message of each subject instead of from
	// new messages on.
	replay bool

	mu      sync.Mutex
	cc      jetstream.ConsumeContext
	name    string
	pending map[uint64]struct{} // delivered, not yet settled
	settled uint64              // highest stream sequence settled

	lost      chan struct{}
	missed    atomic.Int32  // missed heartbeats in a row, reset by a delivery
	stopped   chan struct{} // the current consume context ended on its own
	done      chan struct{}
	closeOnce sync.Once
}

func newLiveConsumer(js jetstream.JetStream, note func(string, error), stream, prefix string, base jetstream.ConsumerConfig,
	retryWait, opTimeout time.Duration, handle func(jetstream.Msg) bool) *liveConsumer {
	if retryWait <= 0 {
		retryWait = defaultSubscriptionRetryWait
	}
	if opTimeout <= 0 {
		opTimeout = defaultOperationTimeout
	}
	if note == nil {
		note = func(string, error) {}
	}
	return &liveConsumer{
		js: js, note: note, stream: stream, prefix: prefix, base: base,
		retryWait: retryWait, opTimeout: opTimeout, handle: handle,
		started: time.Now(),
		pending: map[uint64]struct{}{},
		lost:    make(chan struct{}, 1),
		stopped: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// start creates the first consumer, from new messages on or, with replay,
// from the last message of each subject, retrying attempts times, and starts
// watching it.
func (l *liveConsumer) start(attempts int) error {
	cfg := l.base
	cfg.DeliverPolicy = jetstream.DeliverNewPolicy
	if l.replay {
		cfg.DeliverPolicy = jetstream.DeliverLastPerSubjectPolicy
	}
	var err error
	for i := 0; i < max(attempts, 1); i++ {
		if err = l.consume(cfg); err == nil {
			go l.supervise()
			return nil
		}
		select {
		case <-l.busClosed: // nil when unset: never ready
			return fmt.Errorf("bus: subscribe %s: the bus is closing: %w", cfg.FilterSubject, err)
		case <-l.done:
			return err
		case <-time.After(l.retryWait):
		}
	}
	return err
}

// consume creates one consumer with cfg and starts consuming it.
func (l *liveConsumer) consume(cfg jetstream.ConsumerConfig) error {
	cfg.Name = fmt.Sprintf("%s-%s", l.prefix, uuid.New().String())
	ctx, cancel := context.WithTimeout(context.Background(), l.opTimeout)
	defer cancel()
	if l.replicas != nil {
		r, err := l.replicas(ctx)
		l.note(cfg.FilterSubject, err)
		if err != nil {
			return fmt.Errorf("bus: replica count of the consumer for %s: %w", cfg.FilterSubject, err)
		}
		cfg.Replicas = r
	}
	cons, err := l.js.CreateOrUpdateConsumer(ctx, l.stream, cfg)
	l.note(cfg.FilterSubject, err)
	if err != nil {
		return fmt.Errorf("bus: create consumer for %s: %w", cfg.FilterSubject, err)
	}
	// The server sends a heartbeat every operation timeout on an idle pull,
	// so a consumer lost with its server is noticed after two of them
	// instead of the client's default 30 seconds. A pull lasts four, the
	// least the client allows for that heartbeat.
	cc, err := cons.Consume(l.deliver, jetstream.ConsumeErrHandler(l.onError),
		jetstream.PullHeartbeat(l.opTimeout), jetstream.PullExpiry(4*l.opTimeout))
	if err != nil {
		l.deleteConsumer(cfg.Name)
		return fmt.Errorf("bus: consume %s: %w", cfg.FilterSubject, err)
	}
	l.mu.Lock()
	l.cc, l.name = cc, cfg.Name
	l.mu.Unlock()
	go l.watch(cc)
	return nil
}

// watch signals when cc ends while it is still the current one: the client
// stops consuming on some errors (a closed connection, a deleted consumer),
// and then no heartbeat error follows to tell the subscription.
func (l *liveConsumer) watch(cc jetstream.ConsumeContext) {
	select {
	case <-l.done:
		return
	case <-cc.Closed():
	}
	l.mu.Lock()
	current := l.cc == cc
	l.mu.Unlock()
	if !current {
		return
	}
	select {
	case l.stopped <- struct{}{}:
	default:
	}
}

func (l *liveConsumer) deliver(msg jetstream.Msg) {
	l.missed.Store(0)
	var seq uint64
	if md, err := msg.Metadata(); err == nil {
		seq = md.Sequence.Stream
	}
	if seq > 0 {
		l.mu.Lock()
		l.pending[seq] = struct{}{}
		l.mu.Unlock()
	}
	settled := l.handle(msg)
	if seq == 0 || !settled {
		return
	}
	l.mu.Lock()
	delete(l.pending, seq)
	if seq > l.settled {
		l.settled = seq
	}
	l.mu.Unlock()
}

// onError runs on the client's consume goroutine, so it only signals.
func (l *liveConsumer) onError(_ jetstream.ConsumeContext, err error) {
	slog.Debug("NATS Bus: consume error", "stream", l.stream, "subject", l.base.FilterSubject, "error", err)
	if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrNoHeartbeat) {
		select {
		case l.lost <- struct{}{}:
		default:
		}
	}
}

// supervise replaces the consumer each time it is gone: at once when its
// consume context ended on its own, or after the heartbeat check when it
// only missed heartbeats. One death can signal both channels (the consume
// context ends and a heartbeat error fires), so both are drained before
// deciding: the consumer is replaced once, not twice.
func (l *liveConsumer) supervise() {
	for {
		ended, ok := l.awaitIncident()
		if !ok {
			return
		}
		if ended {
			slog.Warn("NATS Bus: consuming stopped on its own, replacing the consumer",
				"stream", l.stream, "subject", l.base.FilterSubject)
			l.replace()
			continue
		}
		gone, why := l.gone()
		missed := l.missed.Add(1)
		if !gone && int(missed) < l.missedLimit() {
			// A missed heartbeat with the consumer still listed, or a lookup
			// that did not answer, can be a slow link: keep pulling, for a
			// few rounds.
			slog.Info("NATS Bus: a consumer missed its heartbeat, keeping it for now",
				"stream", l.stream, "subject", l.base.FilterSubject, "missed", missed, "lookup", why)
			continue
		}
		// Gone, or silent for missedLimit rounds in a row: a
		// consumer that delivers nothing, not even a heartbeat, is broken
		// whatever the lookup says.
		l.missed.Store(0)
		l.replace()
	}
}

// awaitIncident waits for the next death signal: a consume context that
// ended on its own (ended true), or a missed heartbeat (ended false). One
// death can queue both, so the other channel is drained before returning:
// whatever is pending belongs to this incident, not the next one. ok is
// false when the subscription or the bus ended first.
func (l *liveConsumer) awaitIncident() (ended bool, ok bool) {
	select {
	case <-l.done:
		return false, false
	case <-l.busClosed: // nil when unset: never ready
		return false, false
	case <-l.stopped:
		ended = true
	case <-l.lost:
	}
	select {
	case <-l.stopped:
		ended = true
	default:
	}
	select {
	case <-l.lost:
	default:
	}
	return ended, true
}

// gone asks the cluster whether the current consumer still exists. why is
// the lookup's answer, for the log.
func (l *liveConsumer) gone() (gone bool, why error) {
	l.mu.Lock()
	name := l.name
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), l.opTimeout)
	defer cancel()
	_, err := l.js.Consumer(ctx, l.stream, name)
	l.note(l.base.FilterSubject, err)
	if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrConsumerDeleted) {
		return true, err
	}
	// An ephemeral consumer has one replica. While its server is down the
	// cluster still lists it, as offline, and it comes back only with that
	// server: for this subscription it is gone.
	var apiErr *jetstream.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == errCodeConsumerOffline, err
}

// defaultMissedHeartbeats is the default of snake.consumer_missed_heartbeats,
// for a bus built without options: how many missed heartbeats in a row (two
// heartbeat intervals each) replace a consumer that the cluster still lists,
// or that a lookup cannot reach. A healthy idle consumer sends heartbeats, so
// it never gets there.
const defaultMissedHeartbeats = 2

func (l *liveConsumer) missedLimit() int {
	if l.maxMissed > 0 {
		return l.maxMissed
	}
	return defaultMissedHeartbeats
}

// errCodeConsumerOffline is the JetStream API error code of a consumer whose
// server is down ("consumer is offline"). The client library names no
// constant for it.
const errCodeConsumerOffline jetstream.ErrorCode = 10119

// resumeConfig starts a replacement at the first message not settled.
func (l *liveConsumer) resumeConfig() (jetstream.ConsumerConfig, uint64) {
	cfg := l.base
	l.mu.Lock()
	defer l.mu.Unlock()
	from := l.settled + 1
	for seq := range l.pending {
		if seq < from {
			from = seq
		}
	}
	if l.settled == 0 && len(l.pending) == 0 {
		if l.replay {
			// Nothing delivered yet: the replay starts over.
			cfg.DeliverPolicy = jetstream.DeliverLastPerSubjectPolicy
			return cfg, 0
		}
		// Nothing delivered yet: everything since the subscription began.
		cfg.DeliverPolicy = jetstream.DeliverByStartTimePolicy
		started := l.started
		cfg.OptStartTime = &started
		return cfg, 0
	}
	cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
	cfg.OptStartSeq = from
	return cfg, from
}

// replace stops the lost consumer and creates a new one, retrying until it
// succeeds, the subscription ends, or the bus closes.
func (l *liveConsumer) replace() {
	l.mu.Lock()
	old, oldName := l.cc, l.name
	l.cc = nil // its end is this replacement, not a fault for watch
	l.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	wait := l.retryWait
	for attempt := 1; ; attempt++ {
		cfg, from := l.resumeConfig()
		err := l.consume(cfg)
		if err == nil {
			select {
			case <-l.done:
				// Unsubscribed while this one was being created.
				_ = l.Unsubscribe()
				return
			default:
			}
			slog.Warn("NATS Bus: a consumer was lost with its server, replaced it",
				"stream", l.stream, "subject", l.base.FilterSubject, "lost", oldName, "resume_seq", from)
			return
		}
		// The first failure is worth a warning; the retries while the
		// cluster settles are not, beyond one line every ten.
		if attempt == 1 {
			slog.Warn("NATS Bus: replacing a lost consumer failed, retrying",
				"stream", l.stream, "subject", l.base.FilterSubject, "error", err)
		} else if attempt%10 == 0 {
			slog.Info("NATS Bus: still replacing a lost consumer",
				"stream", l.stream, "subject", l.base.FilterSubject, "attempts", attempt, "error", err)
		}
		select {
		case <-l.done:
			return
		case <-l.busClosed: // nil when unset: never ready
			return
		case <-time.After(wait):
		}
		// Back off up to one operation timeout between attempts.
		wait = min(2*wait, l.opTimeout)
	}
}

func (l *liveConsumer) deleteConsumer(name string) {
	if l.keep {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.opTimeout)
	defer cancel()
	if err := l.js.DeleteConsumer(ctx, l.stream, name); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
		slog.Debug("NATS Bus: could not delete an ephemeral consumer", "stream", l.stream, "consumer", name, "error", err)
	}
}

// Unsubscribe stops the subscription and deletes its consumer.
func (l *liveConsumer) Unsubscribe() error {
	l.closeOnce.Do(func() { close(l.done) })
	l.mu.Lock()
	cc, name := l.cc, l.name
	l.mu.Unlock()
	if cc != nil {
		cc.Stop()
	}
	if name != "" {
		l.deleteConsumer(name)
	}
	return nil
}
