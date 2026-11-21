// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package kvstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
)

// clusterKeyKey holds the public half of the key every Mixer of the cluster
// signs passports with. It sits outside the entity, name and scenario
// prefixes, so listings and the sweep never see it.
const clusterKeyKey = "cluster.signing_key"

// ErrClusterKeyMismatch is returned when this Mixer's cluster.key is not the
// one the rest of the cluster signs with.
var ErrClusterKeyMismatch = errors.New("kvstore: this Mixer's cluster.key differs from the one the cluster signs with")

// CheckClusterKey records the cluster's signing public key on first use and
// refuses a Mixer whose key differs.
//
// A Rack pins the key of the first passport it receives and refuses any
// other. If each Mixer signed with its own key, a Rack that fails over to
// another Mixer would reject that Mixer's passport and stop. Every Mixer of
// a cluster must therefore hold the same cluster.key.
func CheckClusterKey(ctx context.Context, b Bucket, public []byte) error {
	value := []byte(base64.StdEncoding.EncodeToString(public))
	_, err := b.Create(ctx, clusterKeyKey, value)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errConflict) {
		return fmt.Errorf("kvstore: cluster key: record: %w", err)
	}
	stored, _, err := b.Get(ctx, clusterKeyKey)
	if err != nil {
		return fmt.Errorf("kvstore: cluster key: read: %w", err)
	}
	if !bytes.Equal(stored, value) {
		return fmt.Errorf("%w: copy the same cluster.key to every Mixer before starting it", ErrClusterKeyMismatch)
	}
	return nil
}
