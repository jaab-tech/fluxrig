// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// BucketConfig describes the two registry buckets. They get their own
// settings, never the Snake's general KV ones: a TTL meant for other buckets
// would expire Rack records.
type BucketConfig struct {
	Registry     string
	Liveness     string
	Replicas     int
	MaxValueSize int
	// LivenessTTL is the liveness bucket's max age: how long after its last
	// heartbeat a Rack still counts as online.
	LivenessTTL time.Duration
}

// EnsureBuckets creates or updates both buckets and returns them.
func EnsureBuckets(ctx context.Context, js jetstream.JetStream, cfg BucketConfig) (reg, live Bucket, err error) {
	replicas := cfg.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	regKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:       cfg.Registry,
		Description:  "fluxrig registry: entities, names, scenarios, active pointer",
		Storage:      jetstream.FileStorage,
		Replicas:     replicas,
		History:      1,
		MaxValueSize: int32(cfg.MaxValueSize), //nolint:gosec // bounded by config validation
	})
	if err != nil {
		return nil, nil, fmt.Errorf("kvstore: bucket %s: %w", cfg.Registry, err)
	}
	liveKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      cfg.Liveness,
		Description: "fluxrig liveness: one key per entity, expired by max age",
		Storage:     jetstream.FileStorage,
		Replicas:    replicas,
		History:     1,
		TTL:         cfg.LivenessTTL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("kvstore: bucket %s: %w", cfg.Liveness, err)
	}
	return NewJetStreamBucket(regKV), NewJetStreamBucket(liveKV), nil
}
