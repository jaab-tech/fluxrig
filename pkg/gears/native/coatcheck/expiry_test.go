// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// These tests run the daemon on a real JetStream: what they cover is the race
// between its timer and the expiry of the server, which a mock does not have.

const expiryBucket = "expiry"

func expiryBus(t *testing.T) bus.Bus {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: "expiry", StoreDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	b := bus.NewNatsBus("flux")
	require.NoError(t, b.Connect(s.ClientURL(), bus.ConnectOptions{Name: "expiry"}))
	t.Cleanup(b.Close)
	return b
}

func newExpiryDaemon(t *testing.T, b bus.Bus, maxTTL string) *CoatCheckGear {
	t.Helper()
	g := New().(*CoatCheckGear)
	require.NoError(t, g.Init(&mockGearContext{bus: b, config: map[string]any{
		"mode": ModeDaemon, "bucket": expiryBucket, "default_ttl": "1s", "max_ttl": maxTTL, "include_values": true,
	}}))
	return g
}

func startExpiryDaemon(t *testing.T, g *CoatCheckGear) <-chan *fluxmsg.FluxMsg {
	t.Helper()
	out := make(chan *fluxmsg.FluxMsg, 256)
	require.NoError(t, g.Start(context.Background(), func(m *fluxmsg.FluxMsg) { out <- m }))
	t.Cleanup(func() { _ = g.Stop() })
	return out
}

func park(t *testing.T, b bus.Bus, key, value string, ttl time.Duration) {
	t.Helper()
	m := fluxmsg.New()
	m.Metadata[fluxmsg.MetaCoatCheckTTL] = ttl.String()
	m.Data["v"] = value
	raw, err := cbor.Marshal(m)
	require.NoError(t, err)
	_, err = b.KV().PutWithTTL(context.Background(), expiryBucket, key, raw, ttl)
	require.NoError(t, err)
}

func nextTimeout(t *testing.T, out <-chan *fluxmsg.FluxMsg, within time.Duration) *fluxmsg.FluxMsg {
	t.Helper()
	select {
	case m := <-out:
		require.Equal(t, "flux.event.timeout", m.Metadata["event.type"])
		return m
	case <-time.After(within):
		t.Fatalf("no timeout event within %v", within)
		return nil
	}
}

// Absence can only be observed over a window.
func noTimeout(t *testing.T, out <-chan *fluxmsg.FluxMsg, window time.Duration) {
	t.Helper()
	select {
	case m := <-out:
		t.Fatalf("a timeout event that must not be sent, for key %q", m.Metadata["timeout.key"])
	case <-time.After(window):
	}
}

func expiredValue(t *testing.T, m *fluxmsg.FluxMsg) any {
	t.Helper()
	data, ok := fluxmsg.AsDataMap(m.Data["expired_ctx"])
	require.True(t, ok, "the event carries the expired value")
	return data["v"]
}

// Every key times out once, also past max_ttl, when what the daemon left in
// the bucket expires in turn.
func TestExpiry_EachKeyTimesOutOnce(t *testing.T) {
	b := expiryBus(t)
	out := startExpiryDaemon(t, newExpiryDaemon(t, b, "3s"))
	const n = 50
	for i := range n {
		park(t, b, fmt.Sprintf("k%d", i), "x", time.Second)
	}
	seen := map[string]bool{}
	for range n {
		key := nextTimeout(t, out, 8*time.Second).Metadata["timeout.key"]
		require.False(t, seen[key], "%s timed out twice", key)
		seen[key] = true
	}
	noTimeout(t, out, 6*time.Second)
}

// slowPutBus shows each stored value late to whoever follows the bucket, so
// the server's expiry comes before the daemon's timer.
type slowPutBus struct {
	bus.Bus
	delay time.Duration
}

func (b slowPutBus) KV() bus.KeyValue { return slowPutKV{b.Bus.KV(), b.delay} }

// The server removes an entry at its TTL. When that is seen before the timer
// fires, it is the expiry, not a delete that cancels it.
func TestExpiry_ServerExpiresBeforeTheTimer(t *testing.T) {
	b := expiryBus(t)
	out := startExpiryDaemon(t, newExpiryDaemon(t, slowPutBus{b, 100 * time.Millisecond}, "3s"))
	park(t, b, "k", "first", time.Second)
	assert.Equal(t, "first", expiredValue(t, nextTimeout(t, out, 5*time.Second)))
	noTimeout(t, out, 6*time.Second)
}

// A daemon that starts takes up what is already parked: every activation of
// a scenario restarts the gears.
func TestExpiry_DaemonStartsWithEntriesParked(t *testing.T) {
	b := expiryBus(t)
	g := newExpiryDaemon(t, b, "1m")
	park(t, b, "k", "parked", 2*time.Second)
	out := startExpiryDaemon(t, g)
	assert.Equal(t, "parked", expiredValue(t, nextTimeout(t, out, 8*time.Second)))
}

// A daemon reports only the expiry of what it saw parked: the marker of an
// entry that expired while no daemon ran looks the same as that of a deleted
// one whose tombstone aged out.
func TestExpiry_MarkerOfAnUnseenKeyIsNotATimeout(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted %v", deleted), func(t *testing.T) {
			b := expiryBus(t)
			g := newExpiryDaemon(t, b, "3s")
			ctx := context.Background()
			park(t, b, "k", "gone", time.Second)
			if deleted {
				require.NoError(t, b.KV().Delete(ctx, expiryBucket, "k"))
			}
			marked := make(chan struct{}, 1)
			sub, err := b.KV().Follow(ctx, expiryBucket, func(rec bus.KVRecord) {
				if rec.Op == bus.KVExpired {
					select {
					case marked <- struct{}{}:
					default:
					}
				}
			})
			require.NoError(t, err)
			select {
			case <-marked:
			case <-time.After(10 * time.Second):
				t.Fatal("the bucket wrote no expiry marker")
			}
			require.NoError(t, sub.Unsubscribe())
			noTimeout(t, startExpiryDaemon(t, g), 2*time.Second)
		})
	}
}

// A timeout that was sent is not sent again by the next daemon.
func TestExpiry_NotRepeatedAfterARestart(t *testing.T) {
	b := expiryBus(t)
	first := newExpiryDaemon(t, slowPutBus{b, 100 * time.Millisecond}, "1m")
	out := startExpiryDaemon(t, first)
	park(t, b, "k", "once", time.Second)
	nextTimeout(t, out, 5*time.Second)
	noTimeout(t, out, time.Second)
	require.NoError(t, first.Stop())
	noTimeout(t, startExpiryDaemon(t, newExpiryDaemon(t, b, "1m")), 2*time.Second)
}

// An explicit delete is not a timeout, nor is the expiry of its tombstone.
func TestExpiry_ExplicitDeleteIsNotATimeout(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("daemon running %v", running), func(t *testing.T) {
			b := expiryBus(t)
			g := newExpiryDaemon(t, b, "3s")
			var out <-chan *fluxmsg.FluxMsg
			if running {
				out = startExpiryDaemon(t, g)
			}
			park(t, b, "k", "x", 2*time.Second)
			require.NoError(t, b.KV().Delete(context.Background(), expiryBucket, "k"))
			if !running {
				out = startExpiryDaemon(t, g)
			}
			noTimeout(t, out, 7*time.Second)
		})
	}
}

// A key parked again times out once, at the TTL of the second park and with
// its value.
func TestExpiry_ReparkTimesOutOnceWithTheLastValue(t *testing.T) {
	b := expiryBus(t)
	out := startExpiryDaemon(t, newExpiryDaemon(t, b, "1m"))
	park(t, b, "k", "first", 2*time.Second)
	noTimeout(t, out, time.Second)
	park(t, b, "k", "second", 2*time.Second)
	noTimeout(t, out, 1700*time.Millisecond)
	assert.Equal(t, "second", expiredValue(t, nextTimeout(t, out, 5*time.Second)))
	noTimeout(t, out, 2*time.Second)
}

func newExpiryGear(t *testing.T, b bus.Bus, cfg map[string]any) (*CoatCheckGear, <-chan *fluxmsg.FluxMsg) {
	t.Helper()
	all := map[string]any{"bucket": expiryBucket, "key_fields": []string{"meta.k"}, "default_ttl": "2s"}
	for k, v := range cfg {
		all[k] = v
	}
	g := New().(*CoatCheckGear)
	require.NoError(t, g.Init(&mockGearContext{bus: b, config: all}))
	out := make(chan *fluxmsg.FluxMsg, 8)
	require.NoError(t, g.Start(context.Background(), func(m *fluxmsg.FluxMsg) { out <- m }))
	t.Cleanup(func() { _ = g.Stop() })
	return g, out
}

func keyedMsg() *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.Metadata["k"] = "k"
	return m
}

// An answered key times out like any other unless restore removes it.
func TestExpiry_DeleteOnRestore(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete_on_restore %v", remove), func(t *testing.T) {
			b := expiryBus(t)
			events := startExpiryDaemon(t, newExpiryDaemon(t, b, "1m"))
			store, _ := newExpiryGear(t, b, map[string]any{"mode": ModeStore})
			restore, restored := newExpiryGear(t, b, map[string]any{"mode": ModeRestore, "delete_on_restore": remove})
			ctx := context.Background()
			_, err := store.Process(ctx, keyedMsg())
			require.NoError(t, err)

			// The message's context ends when its handler returns.
			msgCtx, done := context.WithCancel(ctx)
			_, err = restore.Process(msgCtx, keyedMsg())
			done()
			require.NoError(t, err)
			require.Len(t, restored, 1)

			if !remove {
				_, err = restore.Process(ctx, keyedMsg())
				require.NoError(t, err, "the entry stays for a repeated reply")
				nextTimeout(t, events, 5*time.Second)
				return
			}
			key, err := restore.extractKey(keyedMsg())
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				v, _, getErr := b.KV().Get(ctx, expiryBucket, key)
				return getErr == nil && v == nil
			}, time.Second, 20*time.Millisecond)
			_, err = restore.Process(ctx, keyedMsg())
			require.ErrorContains(t, err, "missing context")
			noTimeout(t, events, 4*time.Second)
		})
	}
}

// reparkBus parks a key again right after it is read.
type reparkBus struct{ bus.Bus }

func (b reparkBus) KV() bus.KeyValue { return reparkKV{b.Bus.KV()} }

type reparkKV struct{ bus.KeyValue }

func (k reparkKV) Get(ctx context.Context, bucket, key string) ([]byte, uint64, error) {
	v, rev, err := k.KeyValue.Get(ctx, bucket, key)
	if err != nil || v == nil {
		return v, rev, err
	}
	_, err = k.PutWithTTL(ctx, bucket, key, v, 10*time.Second)
	return v, rev, err
}

// Removing what was restored does not remove a later park of the same key.
func TestExpiry_DeleteOnRestoreKeepsANewerPark(t *testing.T) {
	b := expiryBus(t)
	newExpiryDaemon(t, b, "1m") // creates the bucket
	store, _ := newExpiryGear(t, b, map[string]any{"mode": ModeStore})
	restore, _ := newExpiryGear(t, reparkBus{b}, map[string]any{"mode": ModeRestore, "delete_on_restore": true})
	ctx := context.Background()
	_, err := store.Process(ctx, keyedMsg())
	require.NoError(t, err)
	_, err = restore.Process(ctx, keyedMsg())
	require.NoError(t, err)
	key, err := restore.extractKey(keyedMsg())
	require.NoError(t, err)
	require.Never(t, func() bool {
		v, _, getErr := b.KV().Get(ctx, expiryBucket, key)
		return getErr != nil || v == nil
	}, time.Second, 50*time.Millisecond)
}
