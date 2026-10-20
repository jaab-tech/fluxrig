// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// slowBus accepts every publish after a fixed delay, as a bus with a round
// trip of that length would.
type slowBus struct {
	*bus.MockBus
	delay time.Duration
	n     atomic.Int64
}

func (b *slowBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	time.Sleep(b.delay) // the simulated round trip, not a wait for a condition
	b.n.Add(1)
	return nil
}

// After a short outage, live emissions on a subject with a backlog must not
// outrun the drain. Without backpressure they parked behind it, the drain
// sent one message per round trip, the outbox filled, and the cap evicted
// messages their gears were told were sent.
func TestOutbox_BackpressureLosesNothingWhenTheBusIsUp(t *testing.T) {
	down := &switchBus{MockBus: bus.NewMockBus()}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(50, 1, OutboxOrderSubject))
	mgr.SetOutboxBackpressure(2 * time.Second)
	busSubject(mgr, "flux.msg.test-rack.g.out")

	// A short outage leaves a backlog.
	const parkedFirst = 5
	for i := 0; i < parkedFirst; i++ {
		require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg(fmt.Sprint(i))))
	}
	waitOutbox(t, mgr, parkedFirst)

	// The bus returns with a 5 ms round trip; four emitters offer far more
	// than one message per round trip.
	up := &slowBus{MockBus: bus.NewMockBus(), delay: 5 * time.Millisecond}
	mgr.SetBus(up)
	var emitted atomic.Int64
	var wg sync.WaitGroup
	stop := time.Now().Add(time.Second)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				if mgr.publishPort(context.Background(), "g", "out", testMsg("live")) == nil {
					emitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// Everything the gears were told was sent reaches the bus.
	want := int64(parkedFirst) + emitted.Load()
	deadline := time.Now().Add(10 * time.Second)
	for up.n.Load() < want || mgr.outbox.len() > 0 {
		require.True(t, time.Now().Before(deadline),
			"delivered %d of %d, %d still parked, %d dropped", up.n.Load(), want, mgr.outbox.len(), func() uint64 { mgr.outbox.mu.Lock(); defer mgr.outbox.mu.Unlock(); return mgr.outbox.dropped }())
		time.Sleep(10 * time.Millisecond) // poll interval
	}
	mgr.outbox.mu.Lock()
	dropped := mgr.outbox.dropped
	mgr.outbox.mu.Unlock()
	assert.Zero(t, dropped, "the cap evicted messages while the bus was up")
	assert.Equal(t, want, up.n.Load())
}

// With the bus down, a live emission does not wait: it parks at once, so a
// gear never stalls on a bus that is not there.
func TestOutbox_NoBackpressureWhileTheBusIsDown(t *testing.T) {
	down := &switchBus{MockBus: bus.NewMockBus()}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(50, 1, OutboxOrderSubject))
	mgr.SetOutboxBackpressure(5 * time.Second)
	busSubject(mgr, "flux.msg.test-rack.g.out")

	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("first")))
	waitOutbox(t, mgr, 1)

	start := time.Now()
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("second")))
	assert.Less(t, time.Since(start), time.Second, "an emission waited on a bus that is down")
	waitOutbox(t, mgr, 2)
}

// SetBus starts the drain in a goroutine. An emission between SetBus and that
// goroutine's first step must already see the drain, or it skips the wait and
// parks at full speed until the cap evicts. So the drain is marked as running
// before SetBus returns, not when its goroutine is scheduled.
func TestOutbox_DrainIsRunningWhenSetBusReturns(t *testing.T) {
	down := &switchBus{MockBus: bus.NewMockBus()}
	mgr := newOutboxManager(t, down)
	require.NoError(t, mgr.SetOutboxConfig(50, 1, OutboxOrderSubject))
	busSubject(mgr, "flux.msg.test-rack.g.out")
	require.NoError(t, mgr.publishPort(context.Background(), "g", "out", testMsg("parked")))
	waitOutbox(t, mgr, 1)

	// A bus that blocks its first publish, so the drain cannot finish first.
	release := make(chan struct{})
	defer close(release)
	mgr.SetBus(&blockingBus{MockBus: bus.NewMockBus(), release: release})

	mgr.outbox.mu.Lock()
	draining := mgr.outbox.draining
	mgr.outbox.mu.Unlock()
	assert.True(t, draining, "SetBus returned before the drain was marked as running")
}

// blockingBus holds every publish until release closes.
type blockingBus struct {
	*bus.MockBus
	release chan struct{}
}

func (b *blockingBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}
