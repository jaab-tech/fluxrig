// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventProvider is an optional capability of a GearContext: a context on a Rack
// has it, and a test context need not. It is the Rack's side of durable events.
// A gear uses it through the pkg/events package.
type EventProvider interface {
	// RackName returns the name of the Rack that hosts this gear.
	RackName() string

	// GearRack returns the Rack of a gear of the scenario: the Rack it is
	// deployed to, or this Rack for a gear that runs on every Rack. A wire
	// resolves its source the same way.
	GearRack(gear string) string

	// PublishEvent stores an event, or parks it until the bus takes it. It does
	// not hold a gear for longer than a publish takes. id makes a second publish
	// of the same event harmless. It returns an error only when the event is lost.
	PublishEvent(ctx context.Context, subject string, data []byte, id uuid.UUID) error
}

// EventAckWaiter is an optional capability of a GearContext that reads events:
// how long a reader waits for the acknowledgement of an event before the event
// is delivered again. An event its consumer sent through a NATS server that
// died comes back after this. Zero leaves the reader its own default.
type EventAckWaiter interface {
	EventAckWait() time.Duration
}

// RawEvent is one encoded event of a batch, with the ID that makes a second
// publish of it harmless.
type RawEvent struct {
	Data []byte
	ID   uuid.UUID
}

// EventBatchPublisher is an optional capability of a GearContext that
// publishes events: it sends several events of one subject without waiting
// for each to be stored before it sends the next. Across a slow link one
// publish costs a round trip, and one at a time a gear could not send its
// events as fast as its connections made them. Like PublishEvent, it parks
// what the bus does not take, keeps the order, and returns an error only for
// events it lost.
type EventBatchPublisher interface {
	PublishEvents(ctx context.Context, subject string, evs []RawEvent) error
}
