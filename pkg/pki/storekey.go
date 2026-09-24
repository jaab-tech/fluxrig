// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	storeKeyInfo   = "fluxrig snake store key v1"
	storeKeyLength = 32
)

// DeriveStoreKey returns the key that encrypts the Mixer's message store when the
// operator gives none: the same cluster key always gives the same one, so a Mixer
// that already has a cluster key encrypts its store with nothing to configure.
//
// The derived key lives beside the data (the cluster key is in the Mixer's data
// directory), so it protects against a copy of the store or a backup taken without
// the key file, and not against the theft of the whole disk. A key file the operator
// keeps elsewhere does.
func (c *ClusterKey) DeriveStoreKey() (string, error) {
	if c == nil || len(c.Private) != ed25519.PrivateKeySize {
		return "", errors.New("pki: cluster private key has the wrong size")
	}
	key, err := hkdf.Key(sha256.New, c.Private.Seed(), nil, storeKeyInfo, storeKeyLength)
	if err != nil {
		return "", fmt.Errorf("pki: derive store key: %w", err)
	}
	return hex.EncodeToString(key), nil
}
