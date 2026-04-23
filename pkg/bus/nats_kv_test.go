// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"sync"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNatsKV_Disconnected(t *testing.T) {
	nb := NewNatsBus("std")
	kv := nb.KV()

	_, err := kv.Put("bucket", "key", []byte("val"))
	assert.Error(t, err)

	_, _, err = kv.Get("bucket", "key")
	assert.Error(t, err)

	err = kv.Delete("bucket", "key")
	assert.Error(t, err)

	_, err = kv.Watch("bucket", ">", nil)
	assert.Error(t, err)

	_, err = kv.Keys("bucket")
	assert.Error(t, err)

	err = kv.EnsureBucket("bucket", "memory", 1, 0)
	assert.Error(t, err)
}

func TestNatsKV_Lifecycle(t *testing.T) {
	// 1. Start ephemeral NATS
	s, err := snake.NewServer(snake.Config{
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
	err = kv.EnsureBucket(bucket, "memory", 1, 1*time.Hour)
	require.NoError(t, err)

	// 4. Put/Get
	val := []byte("hello world")
	rev1, err := kv.Put(bucket, "msg", val)
	require.NoError(t, err)
	assert.True(t, rev1 > 0)

	got, rev2, err := kv.Get(bucket, "msg")
	require.NoError(t, err)
	assert.Equal(t, val, got)
	assert.Equal(t, rev1, rev2)

	// 5. Get Non-Existent
	gotNone, revNone, err := kv.Get(bucket, "unknown")
	require.NoError(t, err)
	assert.Nil(t, gotNone)
	assert.Equal(t, uint64(0), revNone)

	// 6. Keys
	keys, err := kv.Keys(bucket)
	require.NoError(t, err)
	assert.Contains(t, keys, "msg")

	// 7. Delete
	err = kv.Delete(bucket, "msg")
	require.NoError(t, err)

	gotDeleted, _, err := kv.Get(bucket, "msg")
	require.NoError(t, err)
	assert.Nil(t, gotDeleted)
}

func TestNatsKV_Watch(t *testing.T) {
	// 1. Start ephemeral NATS
	s, err := snake.NewServer(snake.Config{
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
	_ = kv.EnsureBucket(bucket, "memory", 1, 0)

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

	sub, err := kv.Watch(bucket, ">", handler)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	// Give NATS a moment to establish the watcher subscription
	time.Sleep(200 * time.Millisecond)

	// 3. Trigger Updates
	_, _ = kv.Put(bucket, "foo.bar", []byte("val1"))
	_, _ = kv.Put(bucket, "foo.baz", []byte("val2"))

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
