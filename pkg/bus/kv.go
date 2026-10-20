// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"time"
)

// KeyValue represents the Distributed KV Store capabilities (Coat Check).
// It abstracts the underlying storage (NATS JetStream KV).
// All methods accept a context so that caller cancellation and deadlines
// are respected — preventing indefinite hangs on NATS partitions.
type KeyValue interface {
	// Put saves a value with an optional revision check.
	// Returns the new revision number.
	// It is PutWithTTL with a zero TTL, which means the bucket's own TTL.
	Put(ctx context.Context, bucket, key string, value []byte) (revision uint64, err error)

	// PutWithTTL saves a value that expires on its own after ttl, independent
	// of the bucket TTL. A zero or negative ttl means the bucket's own TTL.
	// This is what gives a Coat Check entry the TTL its message asked for
	// rather than the bucket's ceiling, and what lets the entry expire while
	// the Daemon that would otherwise time it out is not running.
	PutWithTTL(ctx context.Context, bucket, key string, value []byte, ttl time.Duration) (revision uint64, err error)

	// Get retrieves a value. Returns nil, 0, nil if not found (or specific error).
	Get(ctx context.Context, bucket, key string) (value []byte, revision uint64, err error)

	// Delete removes a key.
	Delete(ctx context.Context, bucket, key string) error

	// Watch subscribes to updates on a bucket.
	// Used by the Daemon Gear for "Watch & Schedule".
	// The keys pattern can be a wildcard (e.g., ">" or "*").
	Watch(ctx context.Context, bucket, keys string, handler KVHandler) (Subscription, error)

	// Keys returns all keys in a bucket.
	// Used by the Daemon on startup to "Reload" timers (Rehydration).
	Keys(ctx context.Context, bucket string) ([]string, error)

	// EnsureBucket creates a bucket if it doesn't exist.
	// Used by the Daemon Gear for "Governance".
	// storage: "file" or "memory".
	EnsureBucket(ctx context.Context, bucket string, storage string, replicas int, ttl time.Duration) error
}

// KVHandler is the callback for Watch updates.
// operation: "PUT" (Created/Updated) or "DEL" (Deleted/Purged).
type KVHandler func(key string, value []byte, operation string)
