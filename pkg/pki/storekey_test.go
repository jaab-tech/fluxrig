// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterKey_DeriveStoreKey(t *testing.T) {
	a, err := GenerateClusterKey()
	require.NoError(t, err)
	b, err := GenerateClusterKey()
	require.NoError(t, err)

	keyA, err := a.DeriveStoreKey()
	require.NoError(t, err)
	again, err := a.DeriveStoreKey()
	require.NoError(t, err)
	keyB, err := b.DeriveStoreKey()
	require.NoError(t, err)

	assert.Equal(t, keyA, again, "the same cluster key always gives the same store key")
	assert.NotEqual(t, keyA, keyB, "another cluster key gives another one")
	assert.Len(t, keyA, 64, "32 bytes in hex")

	_, err = (&ClusterKey{Private: []byte("short")}).DeriveStoreKey()
	assert.Error(t, err)
	_, err = (*ClusterKey)(nil).DeriveStoreKey()
	assert.Error(t, err)
}
