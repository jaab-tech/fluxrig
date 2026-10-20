// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func TestNatsKV_Disconnected(t *testing.T) {
	ctx := context.Background()
	nb := NewNatsBus("std")
	kv := nb.KV()

	_, err := kv.Put(ctx, "bucket", "key", []byte("val"))
	assert.Error(t, err)

	_, _, err = kv.Get(ctx, "bucket", "key")
	assert.Error(t, err)

	err = kv.Delete(ctx, "bucket", "key")
	assert.Error(t, err)

	_, err = kv.Watch(ctx, "bucket", ">", nil)
	assert.Error(t, err)

	_, err = kv.Keys(ctx, "bucket")
	assert.Error(t, err)

	err = kv.EnsureBucket(ctx, "bucket", "memory", 1, 0)
	assert.Error(t, err)
}

func TestNatsKV_Lifecycle(t *testing.T) {
	ctx := context.Background()

	// 1. Start ephemeral NATS
	s, err := snake.NewServer(ctx, snake.Config{
		Port:        -1,
		ClusterName: "kv-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	// 2. Setup Bus
	nb := NewNatsBus("flux")
	err = nb.Connect(s.ClientURL(), ConnectOptions{
		Name: "test-kv-client",
	})
	require.NoError(t, err)
	defer nb.Close()

	kv := nb.KV()
	bucket := "test_bucket"

	// 3. Ensure Bucket
	err = kv.EnsureBucket(ctx, bucket, "memory", 1, 1*time.Hour)
	require.NoError(t, err)

	// 4. Put/Get
	val := []byte("hello world")
	rev1, err := kv.Put(ctx, bucket, "msg", val)
	require.NoError(t, err)
	assert.True(t, rev1 > 0)

	got, rev2, err := kv.Get(ctx, bucket, "msg")
	require.NoError(t, err)
	assert.Equal(t, val, got)
	assert.Equal(t, rev1, rev2)

	// 5. Get Non-Existent
	gotNone, revNone, err := kv.Get(ctx, bucket, "unknown")
	require.NoError(t, err)
	assert.Nil(t, gotNone)
	assert.Equal(t, uint64(0), revNone)

	// 6. Keys
	keys, err := kv.Keys(ctx, bucket)
	require.NoError(t, err)
	assert.Contains(t, keys, "msg")

	// 7. Delete
	err = kv.Delete(ctx, bucket, "msg")
	require.NoError(t, err)

	gotDeleted, _, err := kv.Get(ctx, bucket, "msg")
	require.NoError(t, err)
	assert.Nil(t, gotDeleted)
}

func TestNatsKV_Watch(t *testing.T) {
	ctx := context.Background()

	// 1. Start ephemeral NATS
	s, err := snake.NewServer(ctx, snake.Config{
		Port:        -1,
		ClusterName: "watch-test",
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	defer s.Shutdown()

	nb := NewNatsBus("flux")
	_ = nb.Connect(s.ClientURL(), ConnectOptions{})
	defer nb.Close()

	kv := nb.KV()
	bucket := "watch_bucket"
	_ = kv.EnsureBucket(ctx, bucket, "memory", 1, 0)

	// 2. Setup Watcher
	var wg sync.WaitGroup
	wg.Add(2) // We expect 2 updates

	updates := make(map[string]string)
	var mu sync.Mutex

	handler := func(key string, value []byte, op string) {
		t.Logf("Watch event: key=%s val=%s op=%s", key, value, op)
		mu.Lock()
		updates[key] = string(value)
		mu.Unlock()
		wg.Done()
	}

	sub, err := kv.Watch(ctx, bucket, ">", handler)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	// Give NATS a moment to establish the watcher subscription
	time.Sleep(200 * time.Millisecond)

	// 3. Trigger Updates
	_, _ = kv.Put(ctx, bucket, "foo.bar", []byte("val1"))
	_, _ = kv.Put(ctx, bucket, "foo.baz", []byte("val2"))

	// 4. Verification
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		mu.Lock()
		assert.Equal(t, "val1", updates["foo.bar"])
		assert.Equal(t, "val2", updates["foo.baz"])
		mu.Unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for watch updates")
	}

	// 5. Unsubscribe verification
	err = sub.Unsubscribe()
	require.NoError(t, err)
}

// newTestKV starts an embedded server and returns a connected bus plus a KV
// handle whose bucket has per-message TTL enabled (EnsureBucket).
func newTestKV(t *testing.T, bucket string, bucketTTL time.Duration) (KeyValue, *NatsBus) {
	t.Helper()
	ctx := context.Background()
	s, err := snake.NewServer(ctx, snake.Config{
		Port:        -1,
		ClusterName: t.Name(),
		StoreDir:    t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)

	nb := NewNatsBus("flux")
	require.NoError(t, nb.Connect(s.ClientURL(), ConnectOptions{Name: t.Name()}))
	t.Cleanup(nb.Close)

	kv := nb.KV()
	require.NoError(t, kv.EnsureBucket(ctx, bucket, "file", 1, bucketTTL))
	return kv, nb
}

// The whole point of the per-key TTL: an entry expires on its own, with no
// Daemon running. Before this a message that asked for a 1s TTL still lived
// for the bucket-wide TTL, so a short-lived key (a PAN) outlived its ticket.
func TestNatsKV_PerKeyTTLExpiresWithoutDaemon(t *testing.T) {
	ctx := context.Background()
	// Bucket ceiling far longer than the per-key TTL, so the entry's own TTL
	// is the only thing that can remove it in the test window.
	kv, _ := newTestKV(t, "kv_ttl_test", 10*time.Minute)

	_, err := kv.PutWithTTL(ctx, "kv_ttl_test", "short", []byte("payload"), 1*time.Second)
	require.NoError(t, err)

	// Present immediately.
	val, _, err := kv.Get(ctx, "kv_ttl_test", "short")
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), val)

	// Gone after its own TTL, not the bucket's 10 minutes.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		val, _, err := kv.Get(ctx, "kv_ttl_test", "short")
		if err == nil && val == nil {
			return // expired
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the entry did not expire on its own per-key TTL; it is still held by the bucket TTL")
}

// A zero-TTL Put must keep the old behavior: it lives for the bucket TTL, not
// for a per-key window. This is what proves the new path did not change the
// default one for every other KV user.
func TestNatsKV_ZeroTTLKeepsBucketTTL(t *testing.T) {
	ctx := context.Background()
	kv, _ := newTestKV(t, "kv_zero_ttl_test", 10*time.Minute)

	_, err := kv.Put(ctx, "kv_zero_ttl_test", "long", []byte("payload"))
	require.NoError(t, err)

	// Still present well past a short window, as the bucket TTL allows.
	time.Sleep(3 * time.Second)
	val, _, err := kv.Get(ctx, "kv_zero_ttl_test", "long")
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), val, "a zero-TTL put must not gain a short per-key expiry")
}

// Re-storing an existing key must not fail: Create returns ErrKeyExists for a
// live key and the value is updated. Coat Check re-parks the same correlation
// key on a retry.
func TestNatsKV_PutWithTTLUpsertsExistingKey(t *testing.T) {
	ctx := context.Background()
	kv, _ := newTestKV(t, "kv_upsert_test", 10*time.Minute)

	_, err := kv.PutWithTTL(ctx, "kv_upsert_test", "k", []byte("first"), 30*time.Second)
	require.NoError(t, err)

	_, err = kv.PutWithTTL(ctx, "kv_upsert_test", "k", []byte("second"), 30*time.Second)
	require.NoError(t, err, "a re-park of an existing key must update, not fail")

	val, _, err := kv.Get(ctx, "kv_upsert_test", "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), val)
}
