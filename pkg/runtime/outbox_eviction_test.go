// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func parkTestEvent(t *testing.T, o *outbox, subject string, marker byte) {
	t.Helper()
	require.True(t, o.parkEvent(subject, &outboxEvent{data: []byte{marker}, id: uuid.New()}))
}

// An item the cap evicted after the drain snapshot was taken is filtered
// before the run is sent: it was counted as dropped already, and sending it
// anyway would defeat the cap. The rest of the run still leaves in one batch.
func TestOutbox_EvictedSnapshotItemIsNotSent(t *testing.T) {
	o := newOutbox(3, 1)
	var batches [][]bus.RawMessage
	o.sendEvents = func(_ context.Context, _ string, msgs []bus.RawMessage) (int, error) {
		cp := append([]bus.RawMessage(nil), msgs...)
		batches = append(batches, cp)
		return len(msgs), nil
	}

	parkTestEvent(t, o, "subj", 'a')
	parkTestEvent(t, o, "subj", 'b')
	parkTestEvent(t, o, "subj", 'c')

	// The drain takes its snapshot here.
	o.mu.Lock()
	pass := o.snapshotLocked()
	epoch := o.evictions
	o.mu.Unlock()

	// Before the run is sent, a live message parks and the cap evicts the
	// oldest snapshot item ('a'): events never take room from traffic, a
	// message takes the place of the oldest parked event.
	o.park("subj", fluxmsg.New())

	queued := make(map[outboxItem]struct{}, len(pass))
	for _, it := range pass {
		queued[it] = struct{}{}
	}
	gone := make(map[outboxItem]bool)
	run := o.eventRun(pass, 0, queued, gone)
	require.Len(t, run, 3, "the snapshot still names the evicted item")

	sent := o.sendEventRun(context.Background(), "subj", run, epoch, gone)
	require.Equal(t, 2, sent)
	require.Len(t, batches, 1)
	var got []byte
	for _, m := range batches[0] {
		got = append(got, m.Data...)
	}
	assert.Equal(t, []byte{'b', 'c'}, got, "the evicted item must not reach the bus")
	assert.True(t, gone[run[0]], "what the run took out stays out for the rest of the pass")
}

// tryDirect reserves the subject: while a batch send is in flight, a live
// emission parks behind it instead of publishing direct and overtaking the
// suffix the batch may yet park. Releasing the reservation lets the subject
// go direct again.
func TestOutbox_TryDirectReservesTheSubject(t *testing.T) {
	o := newOutbox(10, 1)
	require.True(t, o.tryDirect("s1"), "an empty subject may send direct")
	assert.False(t, o.tryDirect("s1"), "a second batch waits for the first instead of overtaking it")
	o.endDirect("s1")
	assert.True(t, o.tryDirect("s1"), "ending the reservation lets the subject go direct again")
	o.endDirect("s1")

	// The reservation is visible to the park check too: while a batch is in
	// flight, a live emission parks behind it.
	require.True(t, o.tryDirect("s2"))
	held, parked := o.parkEventIfHeld("s2", &outboxEvent{data: []byte{'x'}, id: uuid.New()})
	assert.True(t, held, "a batch in flight counts as held")
	assert.True(t, parked)
	o.endDirect("s2")
}
