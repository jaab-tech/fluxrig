// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingBucket hangs every operation until the context ends, as a
// partitioned cluster does: the caller's context is the only way out.
type blockingBucket struct{}

func (b *blockingBucket) wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingBucket) Get(ctx context.Context, _ string) ([]byte, uint64, error) {
	if err := b.wait(ctx); err != nil {
		return nil, 0, err
	}
	return nil, 0, errors.New("unreachable")
}

func (b *blockingBucket) Create(ctx context.Context, _ string, _ []byte) (uint64, error) {
	if err := b.wait(ctx); err != nil {
		return 0, err
	}
	return 0, errors.New("unreachable")
}

func (b *blockingBucket) Update(ctx context.Context, _ string, _ []byte, _ uint64) (uint64, error) {
	if err := b.wait(ctx); err != nil {
		return 0, err
	}
	return 0, errors.New("unreachable")
}

func (b *blockingBucket) Put(ctx context.Context, _ string, _ []byte) (uint64, error) {
	if err := b.wait(ctx); err != nil {
		return 0, err
	}
	return 0, errors.New("unreachable")
}

func (b *blockingBucket) Delete(ctx context.Context, _ string, _ uint64) error {
	return b.wait(ctx)
}

func (b *blockingBucket) Keys(ctx context.Context, _ string) ([]string, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	return nil, errors.New("unreachable")
}

// Every lease operation runs under the lease's own bound, not the caller's
// (often deadline-free) context: against a hung cluster each one fails
// instead of hanging the renew loop, the acquire path or the holder read.
func TestLease_OperationsAreBounded(t *testing.T) {
	// Renew every 2s, so the bound is 1s: the test spends ~1s per op.
	lease := NewLease(&blockingBucket{}, uuid.New(), LeaseConfig{TTL: time.Minute, Renew: 2 * time.Second}, nil, nil)

	run := func(name string, fn func(context.Context) error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fn(context.Background()) }()
		select {
		case err := <-done:
			require.Error(t, err, name)
		case <-time.After(30 * time.Second):
			t.Fatalf("%s did not return: the lease waited on a deadline-free context", name)
		}
	}

	run("Acquire", func(ctx context.Context) error {
		_, err := lease.Acquire(ctx)
		return err
	})
	run("Renew", func(ctx context.Context) error {
		_, err := lease.Renew(ctx)
		return err
	})
	run("Holder", func(ctx context.Context) error {
		_, _, err := lease.Holder(ctx)
		return err
	})
}

// A caller that names a tighter deadline keeps it: the bound only caps,
// never extends.
func TestLease_TighterCallerDeadlineWins(t *testing.T) {
	lease := NewLease(&blockingBucket{}, uuid.New(), LeaseConfig{TTL: time.Minute, Renew: time.Minute}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := lease.Holder(ctx)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 30*time.Second)
}
