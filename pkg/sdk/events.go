// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"

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
