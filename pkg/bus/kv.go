// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import "time"

// KeyValue represents the Distributed KV Store capabilities (Coat Check).
// It abstracts the underlying storage (NATS JetStream KV).
type KeyValue interface {
	// Put saves a value with an optional revision check.
	// Returns the new revision number.
	Put(bucket, key string, value []byte) (revision uint64, err error)

	// Get retrieves a value. Returns nil, 0, nil if not found (or specific error).
	Get(bucket, key string) (value []byte, revision uint64, err error)

	// Delete removes a key.
	Delete(bucket, key string) error

	// Watch subscribes to updates on a bucket.
	// Used by the Daemon Gear for "Watch & Schedule".
	// The keys pattern can be a wildcard (e.g., ">" or "*").
	Watch(bucket, keys string, handler KVHandler) (Subscription, error)

	// Keys returns all keys in a bucket.
	// Used by the Daemon on startup to "Reload" timers (Rehydration).
	Keys(bucket string) ([]string, error)

	// EnsureBucket creates a bucket if it doesn't exist.
	// Used by the Daemon Gear for "Governance".
	// storage: "file" or "memory".
	EnsureBucket(bucket string, storage string, replicas int, ttl time.Duration) error
}

// KVHandler is the callback for Watch updates.
// operation: "PUT" (Created/Updated) or "DEL" (Deleted/Purged).
type KVHandler func(key string, value []byte, operation string)
