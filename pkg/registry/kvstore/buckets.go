// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// BucketConfig describes the registry buckets. They get their own settings,
// never the Snake's general KV ones: a TTL meant for other buckets would
// expire Rack records.
type BucketConfig struct {
	Registry     string
	Liveness     string
	Replicas     int
	MaxValueSize int
	// LivenessTTL is the liveness bucket's max age: how long after its last
	// heartbeat a Rack still counts as online.
	LivenessTTL time.Duration
	// LeaseBucket, when set, is the bucket that holds the write lease. Its max
	// age is LeaseTTL, so a crashed holder's key expires on its own.
	LeaseBucket string
	LeaseTTL    time.Duration
}

// bucketDeadline bounds how long EnsureBuckets retries bucket creation while
// a fresh cluster places replicas. It matches the stream provisioner's window.
const bucketDeadline = 2 * time.Minute

// EnsureBuckets creates or updates the registry and liveness buckets and
// returns them. When LeaseBucket is set it also creates the lease bucket and
// returns it; otherwise the returned lease bucket is nil.
//
// Placing a bucket's replicas needs one member per replica, and on a fresh
// cluster the last member may still be starting. A first failure is therefore
// expected rather than fatal, so creation retries until deadline exactly as
// stream provisioning does. Without this, a three-Mixer cluster starting at
// once made the registry fatal with "context deadline exceeded", which the
// phase-6 testbed caught.
func EnsureBuckets(ctx context.Context, js jetstream.JetStream, cfg BucketConfig) (reg, live, lease Bucket, err error) {
	replicas := cfg.Replicas
	if replicas <= 0 {
		replicas = 1
	}

	deadline := time.Now().Add(bucketDeadline)
	attempt := func(bucket string, kvc jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
		return createBucket(ctx, js, bucket, kvc, deadline)
	}

	regKV, err := attempt(cfg.Registry, jetstream.KeyValueConfig{
		Bucket:       cfg.Registry,
		Description:  "fluxrig registry: entities, names, scenarios, active pointer",
		Storage:      jetstream.FileStorage,
		Replicas:     replicas,
		History:      1,
		MaxValueSize: int32(cfg.MaxValueSize), //nolint:gosec // bounded by config validation
	})
	if err != nil {
		return nil, nil, nil, err
	}
	liveKV, err := attempt(cfg.Liveness, jetstream.KeyValueConfig{
		Bucket:      cfg.Liveness,
		Description: "fluxrig liveness: one key per entity, expired by max age",
		Storage:     jetstream.FileStorage,
		Replicas:    replicas,
		History:     1,
		TTL:         cfg.LivenessTTL,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	reg, live = NewJetStreamBucket(js, regKV), NewJetStreamBucket(js, liveKV)

	if cfg.LeaseBucket != "" {
		leaseKV, errLease := attempt(cfg.LeaseBucket, jetstream.KeyValueConfig{
			Bucket:      cfg.LeaseBucket,
			Description: "fluxrig write lease: one key, expired by max age",
			Storage:     jetstream.FileStorage,
			Replicas:    replicas,
			History:     1,
			TTL:         cfg.LeaseTTL,
		})
		if errLease != nil {
			return nil, nil, nil, errLease
		}
		lease = NewJetStreamBucket(js, leaseKV)
	}
	return reg, live, lease, nil
}

// createBucket creates or updates one bucket, retrying until deadline: on a
// fresh cluster the last member may still be starting, and placing a
// bucket's replicas needs one member per replica.
func createBucket(ctx context.Context, js jetstream.JetStream, bucket string, kvc jetstream.KeyValueConfig, deadline time.Time) (jetstream.KeyValue, error) {
	// Per-attempt bound. The caller's ctx has no deadline, so without this a
	// single stalled create would block forever; with it the attempt fails,
	// the loop waits a second and tries again until the deadline.
	const attemptTimeout = 5 * time.Second
	var lastErr error
	for {
		actx, cancel := context.WithTimeout(ctx, attemptTimeout)
		kv, err := js.CreateOrUpdateKeyValue(actx, kvc)
		cancel()
		if err == nil {
			return kv, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("kvstore: bucket %s: %w", bucket, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("kvstore: bucket %s: %w", bucket, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// SpecBucketConfig describes the shared spec bucket.
type SpecBucketConfig struct {
	Name     string
	Replicas int
	// MaxSize is the largest spec in bytes, the bucket's max value size.
	MaxSize int
}

// EnsureSpecBucket creates or updates the shared spec bucket and returns it.
func EnsureSpecBucket(ctx context.Context, js jetstream.JetStream, cfg SpecBucketConfig) (Bucket, error) {
	replicas := cfg.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	kv, err := createBucket(ctx, js, cfg.Name, jetstream.KeyValueConfig{
		Bucket:       cfg.Name,
		Description:  "fluxrig specs: content-addressed blobs and the version index",
		Storage:      jetstream.FileStorage,
		Replicas:     replicas,
		History:      1,
		MaxValueSize: int32(cfg.MaxSize), //nolint:gosec // bounded by config validation
	}, time.Now().Add(bucketDeadline))
	if err != nil {
		return nil, err
	}
	return NewJetStreamBucket(js, kv), nil
}
