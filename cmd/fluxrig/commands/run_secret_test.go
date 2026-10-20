// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The first passport's secret is kept beside the passport; a re-issued
// passport with no secret leaves it, and the Rack keeps presenting it.
func TestRackSecret_KeptAcrossReissues(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "rack.flux")

	assert.Empty(t, rackSecret(statePath, ""), "no secret before the first passport")

	require.NoError(t, storePassportSecret(statePath, "first-secret"))
	assert.Equal(t, "first-secret", rackSecret(statePath, ""))

	require.NoError(t, storePassportSecret(statePath, ""), "a re-issue without a secret")
	assert.Equal(t, "first-secret", rackSecret(statePath, ""), "the kept secret survives")
	assert.Equal(t, "carried", rackSecret(statePath, "carried"), "a passport's own secret wins")

	info, err := os.Stat(secretPath(statePath))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "readable by the Rack's user only")

	require.NoError(t, removeRackSecret(statePath))
	assert.Empty(t, rackSecret(statePath, ""))
	require.NoError(t, removeRackSecret(statePath), "removing twice is not an error")
}
