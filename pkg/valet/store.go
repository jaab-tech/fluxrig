// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import (
	"context"
	"errors"
)

// ErrTicketNotFound is returned by Store.Get and Store.Take for absent keys.
var ErrTicketNotFound = errors.New("valet: ticket not found")

// Store holds the OPEN tickets of one engine. Retained (redeemed) outcomes are
// engine-internal and never reach the store, so a store implementation only
// deals with in-flight state.
//
// Implementations must be safe for concurrent use. The engine serializes
// composite transitions (park, redeem, expire) with its own lock, so a store
// only needs per-operation atomicity. The in-memory store is the default;
// durable or shared backends implement the same contract.
type Store interface {
	// Insert stores t under t.Key if the key is absent and returns (nil, nil).
	// When a ticket already exists it is returned unchanged as (existing, nil)
	// and t is NOT stored.
	Insert(ctx context.Context, t *Ticket) (existing *Ticket, err error)

	// Get returns the ticket for key, or ErrTicketNotFound.
	Get(ctx context.Context, key string) (*Ticket, error)

	// Take removes and returns the ticket for key, or ErrTicketNotFound.
	Take(ctx context.Context, key string) (*Ticket, error)

	// Purge empties the store, returning the removed tickets (shutdown path).
	Purge(ctx context.Context) ([]*Ticket, error)

	// Len returns the number of stored (open) tickets.
	Len(ctx context.Context) (int, error)
}
