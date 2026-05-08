// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPKI_Certification(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pki_certification_test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := &ClusterKey{Private: priv, Public: pub}

	t.Run("ClusterKey_Persistence_Logic", func(t *testing.T) {
		keyPath := filepath.Join(tmpDir, "cluster.key")
		require.NoError(t, signer.Save(keyPath))

		loaded, err := LoadClusterKey(keyPath)
		require.NoError(t, err)
		assert.Equal(t, signer.Public, loaded.Public)
		assert.Equal(t, signer.Private, loaded.Private)
	})

	t.Run("RackState_Signing_Verification_Logic", func(t *testing.T) {
		state := &RackState{
			MixerID:     uuid.New(),
			MachineID:   uuid.New(),
			Name:        "certification-rack",
			Status:      "active",
			Secret:      "deadbeef-certification-secret",
			MixerPublic: pub,
		}

		env, err := signer.Sign(state)
		require.NoError(t, err)
		require.NotNil(t, env)

		verified, err := env.Verify()
		require.NoError(t, err)
		assert.Equal(t, state.Name, verified.Name)
		assert.Equal(t, state.Secret, verified.Secret)
		assert.Equal(t, pub, verified.MixerPublic)
	})

	t.Run("TamperDetection_Logic", func(t *testing.T) {
		state := &RackState{Name: "valid", MixerPublic: pub}
		env, _ := signer.Sign(state)

		// Tamper with signature, NOT payload (to avoid CBOR unmarshal error)
		env.Signature[len(env.Signature)-1] ^= 0xFF

		_, err := env.Verify()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "signature verification failed")
	})
}
