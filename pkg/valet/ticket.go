// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Engine-level errors. Callers map these onto their error-path reasons
// (e.g. a switch emits timeout / unmatched_reply on its error port).
var (
	// ErrUnmatched reports a Redeem for a key with no ticket at all
	// (typically a late reply after the ticket already expired).
	ErrUnmatched = errors.New("valet: no open ticket for key")

	// ErrAlreadyRedeemed reports a Redeem for a key whose ticket was already
	// redeemed and is inside the retention window (a duplicate reply).
	ErrAlreadyRedeemed = errors.New("valet: ticket already redeemed")

	// ErrDraining reports a Park for a NEW key while the engine is draining.
	// Retransmission attachment and retained replay still succeed during a
	// drain; only new work is refused.
	ErrDraining = errors.New("valet: engine is draining")

	// ErrClosed reports any operation after Close.
	ErrClosed = errors.New("valet: engine is closed")
)

// TicketState is the lifecycle state of a Ticket.
type TicketState uint32

const (
	// TicketOpen: parked, in flight, expiry timer armed.
	TicketOpen TicketState = iota
	// TicketClosed: redeemed; retained for idempotent replay until purge.
	TicketClosed
	// TicketExpired: TTL elapsed without a matching reply.
	TicketExpired
)

func (s TicketState) String() string {
	switch s {
	case TicketOpen:
		return "open"
	case TicketClosed:
		return "closed"
	case TicketExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Ticket is the state parked for one in-flight request.
//
// Key, Destination, ReturnCtx, Request, ParkedAt and Deadline are immutable
// after Park. Outcome and ClosedAt are written once at redemption, before the
// ticket is returned to the redeeming caller. A caller holding a ticket from
// an AttachedOpen park result should read only the immutable fields.
type Ticket struct {
	// Key is the correlation key (built by the caller from a tuple of
	// structured fields).
	Key string

	// Destination is an opaque label for in-flight accounting, typically the
	// output port the request was routed to.
	Destination string

	// ReturnCtx carries the caller's return context (routing metadata needed
	// to send the response home). It should not contain sensitive data.
	ReturnCtx map[string]string

	// Request is the parked original message, surfaced again on expiry so the
	// caller can route it to its error handling.
	Request *fluxmsg.FluxMsg

	// Outcome is the response retained at redemption for idempotent replay.
	Outcome *fluxmsg.FluxMsg

	ParkedAt time.Time
	Deadline time.Time
	ClosedAt time.Time

	// state is accessed atomically: timer goroutines transition tickets while
	// callers may inspect them.
	state atomic.Uint32

	// timer is the armed expiry (open) or purge (closed) timer. Guarded by the
	// engine mutex.
	timer Timer
}

// State returns the ticket's current lifecycle state.
func (t *Ticket) State() TicketState { return TicketState(t.state.Load()) }

func (t *Ticket) setState(s TicketState) { t.state.Store(uint32(s)) }

// ParkResult tells the caller what a Park call did.
type ParkResult uint8

const (
	// Parked: a new ticket was created; the caller should route the request.
	Parked ParkResult = iota
	// AttachedOpen: the key already has an in-flight ticket (retransmission);
	// the caller must NOT route the request again.
	AttachedOpen
	// ReplayedClosed: the key was recently redeemed; the returned ticket's
	// Outcome holds the retained response to replay.
	ReplayedClosed
)

func (r ParkResult) String() string {
	switch r {
	case Parked:
		return "parked"
	case AttachedOpen:
		return "attached"
	case ReplayedClosed:
		return "replayed"
	default:
		return "unknown"
	}
}
