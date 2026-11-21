// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// delayedAck is a PubAckFuture that resolves after delay.
type delayedAck struct {
	ok  chan *jetstream.PubAck
	err chan error
}

func (d *delayedAck) Ok() <-chan *jetstream.PubAck { return d.ok }
func (d *delayedAck) Err() <-chan error            { return d.err }
func (d *delayedAck) Msg() *nats.Msg               { return nil }

func ackAfter(delay time.Duration) jetstream.PubAckFuture {
	d := &delayedAck{ok: make(chan *jetstream.PubAck, 1), err: make(chan error, 1)}
	time.AfterFunc(delay, func() { d.ok <- &jetstream.PubAck{} })
	return d
}

// Each acknowledgement gets the full operation timeout: one timer shared by
// the whole batch spent the budget on the first messages and failed the rest
// on a slow link, degrading healthy batches into partial sends with full
// resends. Here every ack arrives inside its own budget but the batch as a
// whole outlasts one budget.
func TestNatsBus_AwaitAcksGivesEachAckTheFullTimeout(t *testing.T) {
	nb := &NatsBus{opTimeout: 300 * time.Millisecond}
	acks := []jetstream.PubAckFuture{ackAfter(200 * time.Millisecond), ackAfter(400 * time.Millisecond), ackAfter(600 * time.Millisecond)}

	sent, err := nb.awaitAcks(context.Background(), "flux.test.batch", acks, len(acks), nil)
	require.NoError(t, err)
	require.Equal(t, len(acks), sent, "every ack arrived inside its own timeout")
}

// A batch that truly stalls still fails, at the position that stalled, and
// reports how many the stream took: the per-message budget bounds the wait
// instead of ending it.
func TestNatsBus_AwaitAcksStillFailsAStalledAck(t *testing.T) {
	nb := &NatsBus{opTimeout: 100 * time.Millisecond}
	acks := []jetstream.PubAckFuture{ackAfter(50 * time.Millisecond), ackAfter(10 * time.Second)}

	sent, err := nb.awaitAcks(context.Background(), "flux.test.batch", acks, len(acks), nil)
	require.Error(t, err)
	require.Equal(t, 1, sent, "only the first ack arrived")
}
