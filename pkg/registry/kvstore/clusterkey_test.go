// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The first Mixer records its key; a Mixer with the same key starts, and one
// with a different key is refused.
func TestCheckClusterKey(t *testing.T) {
	ctx := context.Background()
	b := NewMemoryBucket(0)
	require.NoError(t, CheckClusterKey(ctx, b, []byte("key-a")), "the first Mixer records its key")
	require.NoError(t, CheckClusterKey(ctx, b, []byte("key-a")), "the same key is accepted")
	require.ErrorIs(t, CheckClusterKey(ctx, b, []byte("key-b")), ErrClusterKeyMismatch, "a different key is refused")
}
