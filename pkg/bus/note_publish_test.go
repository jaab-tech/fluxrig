// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A publish that a caller cancels says nothing about the server, so it must
// not count toward a failover: counting it forced a reconnect during
// shutdown.
func TestNatsBus_NotePublishSkipsCancellation(t *testing.T) {
	nb := &NatsBus{failoverAfter: 10}

	nb.notePublish("flux.test.miss", context.Canceled)
	require.Zero(t, nb.pubMisses.Load(), "a cancelled publish must not count as a miss")
}

// Every timeout counts, including one where the caller's own deadline
// expired. When the server is black-holed the socket keeps accepting writes
// and the client's ping queues behind them, so this timeout is the only
// signal left. The latam testbed measured 73 s to move without it, against a
// 60 s gate.
func TestNatsBus_NotePublishCountsTimeout(t *testing.T) {
	nb := &NatsBus{failoverAfter: 10}

	nb.notePublish("flux.test.miss", context.DeadlineExceeded)
	require.EqualValues(t, 1, nb.pubMisses.Load(), "a deadline must count as a miss")

	nb.notePublish("flux.test.miss", nil)
	require.Zero(t, nb.pubMisses.Load(), "a publish that reached a stream resets the count")
}
