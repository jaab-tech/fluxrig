// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On an external NATS a failed provisioning attempt is retried: a create
// request lost while the cluster forms fails one attempt, not the start. The
// dead letter stream goes through the same path as the message stream.
func TestRetryProvision_RetriesOnAnExternalNATS(t *testing.T) {
	s := &Server{externalURL: "nats://127.0.0.1:1"}
	calls := 0
	err := s.retryProvision(context.Background(), "flux-dlq", time.Minute, func() error {
		calls++
		if calls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

// The embedded server answers or fails at once: there is no peer to wait for.
func TestRetryProvision_EmbeddedFailsAtOnce(t *testing.T) {
	s := &Server{}
	calls := 0
	boom := errors.New("boom")
	err := s.retryProvision(context.Background(), "flux-dlq", time.Minute, func() error {
		calls++
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
}

// The retries stop at the timeout and return the last error.
func TestRetryProvision_StopsAtTheTimeout(t *testing.T) {
	s := &Server{externalURL: "nats://127.0.0.1:1"}
	boom := errors.New("still forming")
	err := s.retryProvision(context.Background(), "flux-dlq", 0, func() error { return boom })
	require.ErrorIs(t, err, boom)
}
