// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// Defaults for the outbox, used when the Rack's configuration leaves them
// unset (rack.outbox_max_messages, rack.outbox_drain_retries and
// rack.outbox_order).
const (
	defaultOutboxMaxMessages  = 10000
	defaultOutboxDrainRetries = 3
	defaultOutboxBackpressure = time.Second
	outboxDrainRetryWait      = 200 * time.Millisecond
)

// The drain orders of the outbox (rack.outbox_order).
const (
	// OutboxOrderSubject keeps the parked order within each subject: a message
	// is never forwarded ahead of an older one on the same subject. A
	// reversal parked after its authorization reaches the bus after it.
	OutboxOrderSubject = "subject"
	// OutboxOrderNone forwards whatever the bus takes, in whatever order it
	// takes it. A subject the bus refuses does not hold the others back, and
	// neither does an older message on the same subject.
	OutboxOrderNone = "none"

	defaultOutboxOrder = OutboxOrderSubject
)

// ValidateOutboxOrder reports an error when order names no drain order. The
// empty string selects the default and is valid.
func ValidateOutboxOrder(order string) error {
	switch order {
	case "", OutboxOrderSubject, OutboxOrderNone:
		return nil
	}
	return fmt.Errorf("runtime: outbox order %q: want %q or %q", order, OutboxOrderSubject, OutboxOrderNone)
}

// outboxItem is one bus-bound emission parked while the bus is down.
type outboxItem struct {
	subject string
	msg     *fluxmsg.FluxMsg
}

// outbox is a bounded FIFO of emissions the bus refused. When the bus is down
// a gear emission to a bus-bound subject would otherwise be lost; parked here
// it is forwarded when the bus returns. It is memory only: a Rack restart with
// a non-empty outbox loses it, and that boundary is documented, not hidden.
type outbox struct {
	mu       sync.Mutex
	items    *list.List
	max      int
	retries  int
	ordered  bool
	draining bool
	// again records a drain request that arrived while a drain was running.
	// The running drain takes one more pass for it instead of stopping, so a
	// SetBus that lands while an earlier drain gives up is never lost.
	// againLoud records that one of those requests wanted the warning.
	again     bool
	againLoud bool
	// busState is what the running drain last learned about the bus: unknown
	// until its first publish answers, then up or down after every attempt.
	// A live emission waits for its subject's backlog (see waitClear) unless
	// the bus is known to be down.
	busState drainBusState
	// moved is closed and replaced whenever an item leaves the queue or the
	// drain stops, so a waiting emission re-checks its subject.
	moved chan struct{}
	// wait bounds how long a live emission waits for its subject's backlog
	// before it parks behind it (rack.outbox_backpressure_timeout). Zero
	// parks at once.
	wait    time.Duration
	dropped uint64
	parked  uint64
	drained uint64
}

func newOutbox(maxMessages, drainRetries int) *outbox {
	if maxMessages <= 0 {
		maxMessages = defaultOutboxMaxMessages
	}
	if drainRetries <= 0 {
		drainRetries = defaultOutboxDrainRetries
	}
	return &outbox{
		items:   list.New(),
		max:     maxMessages,
		retries: drainRetries,
		ordered: defaultOutboxOrder == OutboxOrderSubject,
		moved:   make(chan struct{}),
		wait:    defaultOutboxBackpressure,
	}
}

// drainBusState is what the drain last learned about the bus.
type drainBusState int

const (
	busUnknown drainBusState = iota
	busUp
	busDown
)

// signalMovedLocked wakes every emission waiting in waitClear. The caller
// holds o.mu.
func (o *outbox) signalMovedLocked() {
	close(o.moved)
	o.moved = make(chan struct{})
}

// heldLocked reports whether the queue holds a message on subject. The caller
// holds o.mu.
func (o *outbox) heldLocked(subject string) bool {
	for e := o.items.Front(); e != nil; e = e.Next() {
		if e.Value.(outboxItem).subject == subject {
			return true
		}
	}
	return false
}

// waitClear applies backpressure to a live emission on a subject that still
// has parked messages, while the bus is up.
//
// With subject order such an emission cannot go to the bus first, and parking
// it behind the backlog has a cost when the bus is up again: the drain sends
// one message per round trip, so under load the queue fills and the cap
// evicts the oldest messages, which their gears were told were sent. Waiting
// here slows the emitting gear to the drain's pace instead, and nothing is
// lost.
//
// It waits only while a drain is running and the bus is not known to be
// down: until the drain's first publish answers, and while its publishes are
// accepted. With the bus down it returns at once and the emission parks: a gear must
// not stall on a bus that is not there. It returns when the subject is clear,
// when the drain stops or stalls, when wait ends, or when ctx ends. The
// caller still decides with parkIfHeld, which re-checks under the lock.
func (o *outbox) waitClear(ctx context.Context, subject string) {
	o.mu.Lock()
	wait := o.wait
	o.mu.Unlock()
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		o.mu.Lock()
		if !o.ordered || !o.heldLocked(subject) || !o.draining || o.busState == busDown {
			o.mu.Unlock()
			return
		}
		moved := o.moved
		o.mu.Unlock()
		select {
		case <-moved:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// park appends an emission. When full the oldest entry is dropped with a
// counter: blocking the emitting gear while the bus is down would backpressure
// gears into a deadlock, which trades a bounded, counted loss for an unbounded
// stall.
func (o *outbox) park(subject string, msg *fluxmsg.FluxMsg) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.parkLocked(subject, msg)
}

func (o *outbox) parkLocked(subject string, msg *fluxmsg.FluxMsg) {
	for o.items.Len() >= o.max {
		o.items.Remove(o.items.Front())
		o.dropped++
		countOutboxDropped()
	}
	o.items.PushBack(outboxItem{subject: subject, msg: msg})
	o.parked++
	countOutboxParked()
}

// parkIfHeld parks msg when the outbox keeps subject order and already holds
// a message on subject, and reports whether it did. A live emission that went
// straight to the bus would overtake the parked ones, so it queues behind
// them instead. The check and the park share one lock, so a drain cannot
// empty the subject between them.
func (o *outbox) parkIfHeld(subject string, msg *fluxmsg.FluxMsg) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.ordered || !o.heldLocked(subject) {
		return false
	}
	o.parkLocked(subject, msg)
	return true
}

// drain forwards the parked emissions through publish, which is m.bus.Publish
// bound to the current bus. It runs in passes over the queue, oldest first.
//
// A failed publish is one of two things, and the bus says which. A message
// the bus marks as bus.ErrInvalidMessage can never be sent: it is dropped
// with a counter and does not hold anything back. Any other error means the
// bus did not take it, which says nothing about the message: it stays parked.
// With subject order, the rest of that subject also waits for the next pass,
// so nothing overtakes it.
//
// A pass that forwards nothing ends the drain with everything still parked:
// the bus is down again, or refuses every subject left. A dead bus therefore
// never eats the queue, and a message is dropped only for what it is, never
// for when it was sent. The next drain starts on the next SetBus, or on the
// next emission: one the bus accepts, or one parked behind a held subject.
// quiet leaves out the warning when the drain stops. A drain that an emission
// starts while the bus is down stops on every emission, and the warning
// belongs to the drain SetBus starts.
func (o *outbox) drain(ctx context.Context, publish func(context.Context, string, *fluxmsg.FluxMsg) error, log func() *slog.Logger, quiet bool) {
	if !o.beginDrain(quiet) {
		return
	}
	o.runDrain(ctx, publish, log, quiet)
}

// beginDrain marks a drain as running, and reports false when one already
// runs: that drain then makes one more pass. The caller starts the drain's
// loop in a goroutine only after this returns, so an emission that arrives
// between the two already sees the drain and waits for it, instead of
// parking behind the backlog at full speed until the cap evicts.
func (o *outbox) beginDrain(quiet bool) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.draining {
		o.again = true
		o.againLoud = o.againLoud || !quiet
		return false
	}
	o.draining = true
	o.busState = busUnknown
	return true
}

// runDrain is the drain's loop. beginDrain has marked it as running.
func (o *outbox) runDrain(ctx context.Context, publish func(context.Context, string, *fluxmsg.FluxMsg) error, log func() *slog.Logger, quiet bool) {

	stop := func() {
		o.mu.Lock()
		o.draining = false
		o.busState = busUnknown
		o.again = false
		o.againLoud = false
		o.signalMovedLocked()
		o.mu.Unlock()
	}

	for {
		if ctx.Err() != nil {
			stop()
			return
		}
		// The snapshot and the end of the drain share one lock, so a message
		// parked behind a held subject is either in this pass or sees the
		// drain finished and starts a new one.
		o.mu.Lock()
		if o.items.Len() == 0 {
			o.draining = false
			o.busState = busUnknown
			o.again = false
			o.againLoud = false
			o.signalMovedLocked()
			o.mu.Unlock()
			return
		}
		pass := o.snapshotLocked()
		ordered := o.ordered
		o.mu.Unlock()

		var forwarded int
		held := make(map[string]bool)
		for _, it := range pass {
			if ctx.Err() != nil {
				stop()
				return
			}
			if ordered && held[it.subject] {
				continue
			}
			err := o.publishWithRetries(ctx, publish, it)
			switch {
			case errors.Is(err, errOutboxEvicted):
				// The cap evicted it after the snapshot. It is counted as
				// dropped already, and it is not sent.
			case err == nil:
				o.remove(it, false)
				forwarded++
			case errors.Is(err, bus.ErrInvalidMessage):
				o.remove(it, true)
				forwarded++
				log().Error("outbox: dropping a message the bus can never accept",
					"subject", it.subject, "error", err)
			default:
				held[it.subject] = true
			}
		}

		if forwarded == 0 {
			o.mu.Lock()
			if o.again {
				o.again = false
				quiet = quiet && !o.againLoud
				o.againLoud = false
				o.mu.Unlock()
				continue
			}
			o.draining = false
			o.busState = busUnknown
			o.againLoud = false
			o.signalMovedLocked()
			parked := o.items.Len()
			o.mu.Unlock()
			if !quiet {
				log().Warn("outbox: drain stopped, the bus refuses every parked message; keeping them",
					"parked", parked)
			}
			return
		}
	}
}

// errOutboxEvicted reports that the cap evicted an item while a drain held it
// in its snapshot.
var errOutboxEvicted = errors.New("outbox: evicted by the cap")

// publishWithRetries tries one item up to retries+1 times with a wait between
// attempts, and returns the last error when all fail. A message the bus marks
// as invalid is not tried again: the answer cannot change. Before each attempt
// it checks the item is still queued, and returns errOutboxEvicted when the
// cap took it.
func (o *outbox) publishWithRetries(ctx context.Context, publish func(context.Context, string, *fluxmsg.FluxMsg) error, it outboxItem) error {
	var err error
	for attempt := 0; attempt <= o.retries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !o.holds(it) {
			return errOutboxEvicted
		}
		err = publish(ctx, it.subject, it.msg)
		switch {
		case err == nil:
			o.setBusState(busUp)
			return nil
		case errors.Is(err, bus.ErrInvalidMessage):
			// The bus answered: it is up, the message is what is wrong.
			o.setBusState(busUp)
			return err
		}
		o.setBusState(busDown)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(outboxDrainRetryWait):
		}
	}
	return err
}

// snapshotLocked copies the current queue so a pass iterates a fixed list
// while the live list is mutated by remove and by concurrent parks. The caller
// holds o.mu.
func (o *outbox) snapshotLocked() []outboxItem {
	out := make([]outboxItem, 0, o.items.Len())
	for e := o.items.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(outboxItem))
	}
	return out
}

// holds reports whether the item is still queued.
func (o *outbox) holds(it outboxItem) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for e := o.items.Front(); e != nil; e = e.Next() {
		if e.Value.(outboxItem).msg == it.msg {
			return true
		}
	}
	return false
}

// remove takes an item out of the queue (matched by the message pointer, so a
// concurrent park cannot make the match ambiguous) and counts it as forwarded
// or as dropped. An item the cap already evicted is not counted twice.
func (o *outbox) remove(it outboxItem, dropped bool) {
	o.mu.Lock()
	var found bool
	for e := o.items.Front(); e != nil; e = e.Next() {
		if e.Value.(outboxItem).msg == it.msg {
			o.items.Remove(e)
			found = true
			break
		}
	}
	if found {
		if dropped {
			o.dropped++
		} else {
			o.drained++
		}
		o.signalMovedLocked()
	}
	o.mu.Unlock()
	if !found {
		return
	}
	if dropped {
		countOutboxDropped()
	} else {
		countOutboxDrained()
	}
}

// setBusState records what the drain learned about the bus, and wakes the
// emissions waiting in waitClear when it changed.
func (o *outbox) setBusState(st drainBusState) {
	o.mu.Lock()
	if o.busState != st {
		o.busState = st
		o.signalMovedLocked()
	}
	o.mu.Unlock()
}

// len returns the parked count. For tests and the depth gauge.
func (o *outbox) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.items.Len()
}

// countOutboxParked records one parked emission. The telemetry registry may be
// absent (unit tests, offline start), in which case counters are skipped.
func countOutboxParked() {
	if tm := telemetry.GetMetrics(); tm != nil {
		tm.OutboxParked.Add(context.Background(), 1)
		tm.OutboxDepth.Add(context.Background(), 1)
	}
}

func countOutboxDrained() {
	if tm := telemetry.GetMetrics(); tm != nil {
		tm.OutboxDrained.Add(context.Background(), 1)
		tm.OutboxDepth.Add(context.Background(), -1)
	}
}

func countOutboxDropped() {
	if tm := telemetry.GetMetrics(); tm != nil {
		tm.OutboxDropped.Add(context.Background(), 1)
		tm.OutboxDepth.Add(context.Background(), -1)
	}
}
