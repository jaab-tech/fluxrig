// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// natsKV implements the KeyValue interface using NATS JetStream KV.
type natsKV struct {
	js  jetstream.JetStream
	bus *NatsBus // how a followed bucket survives the loss of its consumer
}

// The headers and values by which a bucket record says what it is.
const (
	kvOperationHeader = "KV-Operation"
	kvOperationDelete = "DEL"
	kvOperationPurge  = "PURGE"
	markerMaxAge      = "MaxAge"
)

// validKVKey is the key syntax of the NATS client, which checks it only in
// its own calls. PutWithTTL publishes to the subject of the key itself.
var validKVKey = regexp.MustCompile(`^[-/_=\.a-zA-Z0-9]+$`)

func (k *natsKV) Put(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	return k.PutWithTTL(ctx, bucket, key, value, 0)
}

func (k *natsKV) PutWithTTL(ctx context.Context, bucket, key string, value []byte, ttl time.Duration) (uint64, error) {
	if k.js == nil {
		return 0, errors.New("nats bus not connected")
	}

	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return 0, fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}

	if ttl <= 0 {
		return kv.Put(ctx, key, value)
	}

	// Of the client's KV calls only Create carries a TTL, and Create refuses
	// a live key. A bucket keeps one record per key, so a Put over a live key
	// replaces the record and its TTL with none. Publishing to the subject of
	// the key is that same Put, with the TTL.
	if !validKVKey.MatchString(key) || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return 0, fmt.Errorf("bus: put %s: %w", bucket, jetstream.ErrInvalidKey)
	}
	msg := &nats.Msg{Subject: "$KV." + kv.Bucket() + "." + key, Data: value, Header: nats.Header{}}
	ack, err := k.js.PublishMsg(ctx, msg, jetstream.WithMsgTTL(wholeSeconds(ttl)))
	if err != nil {
		return 0, fmt.Errorf("bus: put %s with a TTL: %w", bucket, err)
	}
	return ack.Sequence, nil
}

// wholeSeconds rounds a TTL up to what the server can count: it refuses less
// than a second and drops any fraction of one.
func wholeSeconds(ttl time.Duration) time.Duration {
	whole := ttl.Truncate(time.Second)
	if whole < ttl {
		whole += time.Second
	}
	return whole
}

func (k *natsKV) PurgeRevision(ctx context.Context, bucket, key string, revision uint64) error {
	if k.js == nil {
		return errors.New("nats bus not connected")
	}
	kv, err := k.js.KeyValue(ctx, bucket)
	if err != nil {
		return fmt.Errorf("failed to get bucket %s: %w", bucket, err)
	}
	if err := kv.Purge(ctx, key, jetstream.LastRevision(revision)); err != nil {
		return fmt.Errorf("bus: purge revision %d in %s: %w", revision, bucket, err)
	}
	return nil
}

func (k *natsKV) Follow(ctx context.Context, bucket string, handler func(KVRecord)) (Subscription, error) {
	if k.js == nil || k.bus == nil {
		return nil, errors.New("nats bus not connected")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("bus: follow %s: %w", bucket, err)
	}
	prefix := "$KV." + bucket + "."
	n := k.bus
	live := newLiveConsumer(k.js, n.noteRequest, "KV_"+bucket, "flux-kv", jetstream.ConsumerConfig{
		FilterSubject:     prefix + ">",
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: n.inactiveThreshold,
	}, n.retryWait, n.opTimeout, func(msg jetstream.Msg) bool {
		rec := KVRecord{Key: strings.TrimPrefix(msg.Subject(), prefix), Value: msg.Data(), Op: recordOp(msg.Headers())}
		if md, err := msg.Metadata(); err == nil {
			rec.Revision = md.Sequence.Stream
		}
		handler(rec)
		return true
	})
	live.maxMissed = n.missedHeartbeats
	live.keep = n.keepConsumers
	live.busClosed = n.closed
	live.replay = true
	if err := live.start(1); err != nil {
		return nil, fmt.Errorf("bus: follow %s: %w", bucket, err)
	}
	return live, nil
}

func recordOp(h nats.Header) KVOp {
	switch h.Get(kvOperationHeader) {
	case kvOperationDelete:
		return KVDeleted
	case kvOperationPurge:
		return KVPurged
	}
	switch h.Get(jetstream.MarkerReasonHeader) {
	case "":
		return KVPut
	case markerMaxAge:
		return KVExpired
	default: // removed or purged by an operator
		return KVPurged
	}
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
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil // an empty bucket has no keys
	}
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

	// LimitMarkerTTL is what makes the client set AllowMsgTTL on the stream,
	// which is required for a per-message TTL (jetstream.KeyTTL) to be
	// accepted. It is independent of the entry TTL: it is how long a delete
	// marker is kept for a direct get. Without it the bucket would reject the
	// per-entry TTL and a key would live for the bucket TTL only.
	const deleteMarkerTTL = 2 * time.Minute

	_, err := k.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         bucket,
		Storage:        st,
		Replicas:       replicas,
		TTL:            ttl,
		LimitMarkerTTL: deleteMarkerTTL,
		History:        1, // We only care about current state for CoatCheck
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
