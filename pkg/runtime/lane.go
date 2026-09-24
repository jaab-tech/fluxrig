// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Defaults for the hot lane, used when the Rack's configuration leaves them unset
// (rack.lane_queue_size and rack.lane_send_timeout).
const (
	defaultLaneQueueSize   = 1024
	defaultLaneSendTimeout = 5 * time.Second
	laneQuiescePoll        = 5 * time.Millisecond
)

// unsubscribeWait bounds how long Unsubscribe waits for a handler that was
// already running to finish. Unsubscribe is called from Manager.stopAll while
// m.mu is held, so a handler that never returns must not be allowed to wedge
// every future ApplyScenario/Drain/Shutdown on the Rack: past this wait,
// Unsubscribe gives up on it and returns anyway, the same trade-off Drain
// makes against its own caller-supplied deadline. A var, not a rack.* config
// field yet, so a test can shorten it; making it operator-configurable is a
// reasonable follow-up, not done here.
var unsubscribeWait = 5 * time.Second

// ErrLaneFull is returned to the emitting gear when a subscriber's queue stays full
// for the whole send timeout: the consumer is not keeping up.
var ErrLaneFull = errors.New("hot lane queue is full")

// localLane is the hot lane: it carries the messages between the gears of one Rack
// through memory, with no bus, no serialization to a stream and no disk.
//
// A message reaches each subscriber as its own copy, in the order it was emitted on
// that subject, through a bounded queue. Delivery is at most once. A message that
// is queued when the Rack process dies is lost, which is the price of not storing
// it: nothing about it, a card number included, is written anywhere.
//
// An emitter that finds a queue full waits up to the send timeout for room, and then
// gets ErrLaneFull, as it would get an error from a bus that does not answer. Gears
// that hand messages to each other in a cycle can fill each other's queues, and the
// timeout is what turns that into an error and not a deadlock.
type localLane struct {
	mu          sync.RWMutex
	subs        map[string][]*laneSub
	queueSize   int
	sendTimeout time.Duration
	log         func() *slog.Logger

	pending atomic.Int64 // queued, plus being handled
}

func newLocalLane(queueSize int, sendTimeout time.Duration, log func() *slog.Logger) *localLane {
	l := &localLane{subs: make(map[string][]*laneSub), log: log}
	l.configure(queueSize, sendTimeout)
	return l
}

// configure sets the queue size and the send timeout for the subscriptions made
// from now on. A value that is not positive selects the default.
func (l *localLane) configure(queueSize int, sendTimeout time.Duration) {
	if queueSize <= 0 {
		queueSize = defaultLaneQueueSize
	}
	if sendTimeout <= 0 {
		sendTimeout = defaultLaneSendTimeout
	}
	l.mu.Lock()
	l.queueSize, l.sendTimeout = queueSize, sendTimeout
	l.mu.Unlock()
}

type laneItem struct {
	ctx  context.Context
	data []byte
}

// laneSub is one wire's end of the lane. It implements bus.Subscription.
type laneSub struct {
	lane    *localLane
	subject string
	handler bus.Handler
	queue   chan laneItem
	done    chan struct{}
	once    sync.Once
	stopped sync.WaitGroup
}

// subscribe registers handler for subject and starts the goroutine that feeds it.
func (l *localLane) subscribe(subject string, handler bus.Handler) *laneSub {
	l.mu.Lock()
	s := &laneSub{
		lane:    l,
		subject: subject,
		handler: handler,
		queue:   make(chan laneItem, l.queueSize),
		done:    make(chan struct{}),
	}
	l.subs[subject] = append(l.subs[subject], s)
	l.mu.Unlock()

	s.stopped.Add(1)
	go s.run()
	return s
}

// Unsubscribe stops the subscription. What is still queued is dropped.
func (s *laneSub) Unsubscribe() error {
	s.once.Do(func() {
		close(s.done)

		l := s.lane
		l.mu.Lock()
		kept := l.subs[s.subject][:0]
		for _, other := range l.subs[s.subject] {
			if other != s {
				kept = append(kept, other)
			}
		}
		if len(kept) == 0 {
			delete(l.subs, s.subject)
		} else {
			l.subs[s.subject] = kept
		}
		l.mu.Unlock()
	})

	// A handler already running when close(s.done) fired is not interrupted by
	// it: run's loop only checks s.done between deliveries. s.stopped.Wait()
	// alone would block here for as long as that handler runs, and Unsubscribe
	// is called from Manager.stopAll while m.mu is held, so an unbounded wait
	// here would deadlock every future ApplyScenario/Drain/Shutdown on the
	// Rack over a single stuck gear. Past unsubscribeWait, give up on it: the
	// same trade-off Drain makes against its own deadline, two functions above.
	stopped := make(chan struct{})
	go func() {
		s.stopped.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(unsubscribeWait):
		s.lane.log().Error("hot lane: handler still running past the unsubscribe wait, giving up on it",
			"subject", s.subject, "wait", unsubscribeWait)
	}

	// Whatever was queued and never handled is no longer pending.
	for {
		select {
		case <-s.queue:
			s.lane.pending.Add(-1)
		default:
			return nil
		}
	}
}

func (s *laneSub) run() {
	defer s.stopped.Done()
	for {
		select {
		case <-s.done:
			return
		case item := <-s.queue:
			s.deliver(item)
			s.lane.pending.Add(-1)
		}
	}
}

// deliver decodes the message into a copy of its own and hands it to the handler. A
// handler that panics does not take the subscription down with it.
func (s *laneSub) deliver(item laneItem) {
	var msg fluxmsg.FluxMsg
	if err := cbor.Unmarshal(item.data, &msg); err != nil {
		s.lane.log().Error("hot lane: discarding undecodable message", "subject", s.subject, "bytes", len(item.data), "error", err)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.lane.log().Error("hot lane: handler panic recovered", "subject", s.subject, "panic", r)
		}
	}()
	s.handler(item.ctx, &msg)
}

// publish delivers msg to every subscriber of subject and reports how many there
// were. It validates and encodes the message once, exactly as a publish to the bus
// would, so a message the bus would refuse is refused here as well.
func (l *localLane) publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) (int, error) {
	l.mu.RLock()
	targets := append([]*laneSub(nil), l.subs[subject]...)
	timeout := l.sendTimeout
	l.mu.RUnlock()
	if len(targets) == 0 {
		return 0, nil
	}

	if err := msg.Validate(); err != nil {
		return 0, fmt.Errorf("fluxmsg validation failed: %w", err)
	}
	data, err := cbor.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("hot lane: encode: %w", err)
	}

	// The handler runs after the emitter has moved on, so it must not be cancelled
	// with the emitter's context, and it keeps the trace the emitter was in.
	itemCtx := context.WithoutCancel(ctx)

	// Every subscriber gets its turn even if an earlier one is stuck: a slow or
	// full consumer must not silently deny the message to healthy ones later in
	// the list. The first failure is still reported, once every target has been
	// tried, so the emitter learns delivery was incomplete.
	delivered := 0
	var firstErr error
	for _, s := range targets {
		if err := s.enqueue(ctx, laneItem{ctx: itemCtx, data: data}, timeout); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		delivered++
	}
	return delivered, firstErr
}

func (s *laneSub) enqueue(ctx context.Context, item laneItem, timeout time.Duration) error {
	s.lane.pending.Add(1)

	select {
	case s.queue <- item:
		return nil
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s.queue <- item:
		return nil
	case <-s.done:
		s.lane.pending.Add(-1)
		return nil // the wire was taken down while the message was on its way
	case <-timer.C:
		s.lane.pending.Add(-1)
		return fmt.Errorf("%w: %s, nobody took a message within %s", ErrLaneFull, s.subject, timeout)
	case <-ctx.Done():
		s.lane.pending.Add(-1)
		return ctx.Err()
	}
}

// quiesce waits until nothing is queued and no handler is running, or until ctx
// ends. A graceful stop uses it so that what the gears have already accepted is
// delivered before they are stopped.
func (l *localLane) quiesce(ctx context.Context) error {
	ticker := time.NewTicker(laneQuiescePoll)
	defer ticker.Stop()
	for l.pending.Load() > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("hot lane: %d messages still queued: %w", l.pending.Load(), ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

var _ bus.Subscription = (*laneSub)(nil)
