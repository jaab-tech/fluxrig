// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package valet implements the correlation engine behind fluxrig's
// request/response gears.
//
// A caller parks a ticket for each in-flight request, keyed by a correlation
// key derived from the message (a configured tuple of structured fields, never
// a single wrapping counter). The engine then guarantees:
//
//   - Reply matching: Redeem returns the parked ticket (return context and the
//     original request) for exactly one matching reply.
//   - Retransmission attachment: parking an already-open key attaches to the
//     in-flight ticket instead of creating a duplicate, so a retransmitted
//     request is never routed upstream twice.
//   - Idempotent replay: for a short window after redemption
//     (RetainAfterClose), re-parking the same key returns the retained outcome
//     so a lost response can be answered without re-processing.
//   - Timeout surfacing: an open ticket whose TTL expires is removed and handed
//     to the OnExpire callback; expired keys are not retained, so a late reply
//     is reported as unmatched rather than silently re-matched.
//   - In-flight accounting: open-ticket counts per destination label, the
//     signal a least-loaded routing strategy needs, at no extra cost.
//
// State lives in a pluggable Store; the in-memory store is the default and
// keeps everything in RAM (nothing on disk). Durable and shared backends can
// implement the same interface. Buffer zeroization and memory-locking are part
// of the separate at-rest hardening work and are not performed here yet.
//
// The engine is safe for concurrent use. OnExpire callbacks run on timer
// goroutines and must be safe to call concurrently; panics in the callback are
// recovered and logged so a bad handler cannot take down the host gear.
package valet
