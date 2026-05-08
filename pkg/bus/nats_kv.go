// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// natsKV implements the KeyValue interface using NATS JetStream KV.
type natsKV struct {
	js jetstream.JetStream
}

func (k *natsKV) Put(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	if k.js == nil {
		return 0, errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return 0, fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	rev, err := kv.Put(ctx, key, value)
	if err != nil {
		return 0, err
	}
	return rev, nil
}

func (k *natsKV) Get(ctx context.Context, bucket, key string) ([]byte, uint64, error) {
	if k.js == nil {
		return nil, 0, errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, 0, nil // Not found is not an error for us
		}
		return nil, 0, err
	}

	return entry.Value(), entry.Revision(), nil
}

func (k *natsKV) Delete(ctx context.Context, bucket, key string) error {
	if k.js == nil {
		return errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	return kv.Delete(ctx, key)
}

func (k *natsKV) Watch(ctx context.Context, bucket, keys string, handler KVHandler) (Subscription, error) {
	if k.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	// We use "keys" as the filter. If strict exact match or wildcard is needed.
	// NATS Watch accepts strict key or wildcard.
	var watcher jetstream.KeyWatcher

	if keys == ">" {
		watcher, err = kv.WatchAll(ctx)
	} else {
		watcher, err = kv.Watch(ctx, keys)
	}
	if err != nil {
		return nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)

	// Start a goroutine to consume the updates
	go func() {
		defer cancel()
		for {
			select {
			case <-watchCtx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					return
				}
				if entry == nil {
					continue
				}

				op := "PUT"
				if entry.Operation() == jetstream.KeyValueDelete || entry.Operation() == jetstream.KeyValuePurge {
					op = "DEL"
				}

				handler(entry.Key(), entry.Value(), op)
			}
		}
	}()

	return &natsKVSubscription{
		watcher: watcher,
		cancel:  cancel,
	}, nil

}

func (k *natsKV) Keys(ctx context.Context, bucket string) ([]string, error) {
	if k.js == nil {
		return nil, errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	// NATS Keys() returns KeyLister
	lister, err := kv.Keys(ctx)
	if err != nil {
		return nil, err
	}

	// Technically Keys() return all keys. We assume it fits in memory (for rehydration).
	// If bucket is huge, we might need a stream-based approach, but for CoatCheck pending txns, it's fine.
	return lister, nil
}

func (k *natsKV) EnsureBucket(ctx context.Context, bucket string, storage string, replicas int, ttl time.Duration) error {
	if k.js == nil {
		return errors.New("nats bus not connected")
	}

	// Default to File Storage unless Memory requested
	st := jetstream.FileStorage
	if strings.ToLower(storage) == "memory" {
		st = jetstream.MemoryStorage
	}

	_, err := k.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   bucket,
		Storage:  st,
		Replicas: replicas,
		TTL:      ttl,
		History:  1, // We only care about current state for CoatCheck
	})

	return err
}

// Subscription for Watch
type natsKVSubscription struct {
	watcher jetstream.KeyWatcher
	cancel  context.CancelFunc
}

func (s *natsKVSubscription) Unsubscribe() error {
	s.cancel()
	return s.watcher.Stop()
}
