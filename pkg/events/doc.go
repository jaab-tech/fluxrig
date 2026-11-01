// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package events carries durable events between gears: a fact that a gear
// reports, which other gears may read later, on this Rack or on another one.
//
// The first event is the connection event. A connection gear publishes it with
// a Publisher, and a Publisher never holds the gear. A reader has its own
// durable consumer and takes events with a Reader, so several gears can read
// the same events, and a reader that was down gets what it missed.
//
// The subject, the event names, the fields and the reasons are a public
// contract. Fields are only added.
package events
