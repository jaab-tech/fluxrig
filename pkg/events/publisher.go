// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// DefaultQueueSize is the size of a Publisher's queue.
const DefaultQueueSize = 1024

// Publisher sends the connection events of one gear. Publish only puts the
// event on a bounded queue, and one goroutine sends the queue in order. So a
// socket never waits for the bus, and a connection's opened leaves before its
// closed.
type Publisher struct {
	provider sdk.EventProvider
	gear     string
	subject  string
	log      *slog.Logger

	queue    chan ConnEvent
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewPublisher starts a Publisher for gear. A queueSize that is not positive
// selects DefaultQueueSize.
func NewPublisher(provider sdk.EventProvider, gear string, queueSize int, log *slog.Logger) *Publisher {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	p := &Publisher{
		provider: provider,
		gear:     gear,
		subject:  ConnSubject(provider.RackName(), gear),
		log:      log,
		queue:    make(chan ConnEvent, queueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

// Publish queues an event. It sets the gear, the Rack and the time. A full queue
// drops the event with a Warn, and the connection goes on.
func (p *Publisher) Publish(ev ConnEvent) {
	ev.Gear = p.gear
	ev.Rack = p.provider.RackName()
	ev.TS = time.Now().UTC()
	select {
	case p.queue <- ev:
	default:
		p.log.Warn("connection event dropped, the queue is full", "event", ev.Event, "conn_id", ev.ConnID, "size", cap(p.queue))
	}
}

func (p *Publisher) run() {
	defer close(p.done)
	for {
		select {
		case ev := <-p.queue:
			p.send(ev)
		case <-p.stop:
			for {
				select {
				case ev := <-p.queue:
					p.send(ev)
				default:
					return
				}
			}
		}
	}
}

func (p *Publisher) send(ev ConnEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		p.log.Warn("connection event dropped, it does not encode", "event", ev.Event, "conn_id", ev.ConnID, "error", err)
		return
	}
	if err := p.provider.PublishEvent(context.Background(), p.subject, data, ev.id()); err != nil {
		p.log.Warn("connection event dropped", "event", ev.Event, "conn_id", ev.ConnID, "error", err)
	}
}

// Close sends what is queued and stops the Publisher. It waits until ctx ends,
// and a Publish after Close is lost. It is safe to call twice.
func (p *Publisher) Close(ctx context.Context) error {
	p.stopOnce.Do(func() { close(p.stop) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("events: %d event(s) still queued: %w", len(p.queue), ctx.Err())
	}
}
