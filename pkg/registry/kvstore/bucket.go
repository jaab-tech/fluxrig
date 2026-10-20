// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

var (
	// errKeyNotFound means the key has no live value.
	errKeyNotFound = errors.New("kvstore: key not found")
	// errConflict means a create found the key already present, or an update
	// or delete found a revision other than the one it was given.
	errConflict = errors.New("kvstore: revision conflict")
)

// Bucket is the part of a key-value bucket the registry uses. Every write
// that must not overwrite a concurrent one is a Create (the key must be
// absent) or an Update (the key must still be at the revision read).
type Bucket interface {
	Get(ctx context.Context, key string) (value []byte, revision uint64, err error)
	Create(ctx context.Context, key string, value []byte) (uint64, error)
	Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	// Delete removes key. A non-zero revision makes it conditional.
	Delete(ctx context.Context, key string, revision uint64) error
	// Keys lists the live keys that start with prefix.
	Keys(ctx context.Context, prefix string) ([]string, error)
}

// jsBucket adapts a JetStream key-value bucket.
type jsBucket struct {
	kv jetstream.KeyValue
}

// NewJetStreamBucket wraps a JetStream key-value bucket.
func NewJetStreamBucket(kv jetstream.KeyValue) Bucket {
	return &jsBucket{kv: kv}
}

func mapJSError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
		return errKeyNotFound
	}
	// A create on a present key and an update or delete on a moved revision
	// both come back as the stream's wrong-last-sequence error, which
	// ErrKeyExists matches by code.
	if errors.Is(err, jetstream.ErrKeyExists) {
		return errConflict
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence ||
		apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequenceConstant) {
		return errConflict
	}
	return err
}

func (b *jsBucket) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	e, err := b.kv.Get(ctx, key)
	if err != nil {
		return nil, 0, mapJSError(err)
	}
	return e.Value(), e.Revision(), nil
}

func (b *jsBucket) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	rev, err := b.kv.Create(ctx, key, value)
	return rev, mapJSError(err)
}

func (b *jsBucket) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	rev, err := b.kv.Update(ctx, key, value, revision)
	return rev, mapJSError(err)
}

func (b *jsBucket) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	rev, err := b.kv.Put(ctx, key, value)
	return rev, mapJSError(err)
}

func (b *jsBucket) Delete(ctx context.Context, key string, revision uint64) error {
	if revision == 0 {
		return mapJSError(b.kv.Delete(ctx, key))
	}
	return mapJSError(b.kv.Delete(ctx, key, jetstream.LastRevision(revision)))
}

func (b *jsBucket) Keys(ctx context.Context, prefix string) ([]string, error) {
	lister, err := b.kv.ListKeysFiltered(ctx, prefix+">")
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	// The lister closes its channel without an error when ctx ends, so a
	// cancelled or timed-out listing would read as a complete, shorter one.
	// A caller such as the sweep treats a missing key as absent, so a
	// partial list must be an error.
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, fmt.Errorf("kvstore: list keys %q: %w", prefix, errCtx)
	}
	return keys, nil
}

// MemoryBucket is an in-memory Bucket with the same create, update and
// delete semantics as a JetStream bucket, and an optional max age. It backs
// tests that need a registry without a NATS server.
type MemoryBucket struct {
	mu     sync.Mutex
	rev    uint64
	maxAge time.Duration
	now    func() time.Time
	items  map[string]memItem
}

type memItem struct {
	value   []byte
	rev     uint64
	written time.Time
}

// NewMemoryBucket returns an empty in-memory bucket. A positive maxAge
// expires a key that long after its last write, as a bucket TTL does.
func NewMemoryBucket(maxAge time.Duration) *MemoryBucket {
	return &MemoryBucket{maxAge: maxAge, now: time.Now, items: map[string]memItem{}}
}

// SetClock replaces the bucket's clock. For tests of expiry.
func (m *MemoryBucket) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func (m *MemoryBucket) live(key string) (memItem, bool) {
	it, ok := m.items[key]
	if !ok {
		return memItem{}, false
	}
	if m.maxAge > 0 && m.now().Sub(it.written) >= m.maxAge {
		delete(m.items, key)
		return memItem{}, false
	}
	return it, true
}

func (m *MemoryBucket) write(key string, value []byte) uint64 {
	m.rev++
	m.items[key] = memItem{value: append([]byte(nil), value...), rev: m.rev, written: m.now()}
	return m.rev
}

func (m *MemoryBucket) Get(_ context.Context, key string) ([]byte, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if !ok {
		return nil, 0, errKeyNotFound
	}
	return append([]byte(nil), it.value...), it.rev, nil
}

func (m *MemoryBucket) Create(_ context.Context, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return 0, errConflict
	}
	return m.write(key, value), nil
}

func (m *MemoryBucket) Update(_ context.Context, key string, value []byte, revision uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if !ok || it.rev != revision {
		return 0, errConflict
	}
	return m.write(key, value), nil
}

func (m *MemoryBucket) Put(_ context.Context, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.write(key, value), nil
}

func (m *MemoryBucket) Delete(_ context.Context, key string, revision uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.live(key)
	if revision != 0 && (!ok || it.rev != revision) {
		return errConflict
	}
	delete(m.items, key)
	return nil
}

func (m *MemoryBucket) Keys(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.items {
		if _, ok := m.live(k); ok && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
