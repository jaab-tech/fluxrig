// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"fmt"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// Where the store key came from, for the log.
const (
	storeKeyFromFile    = "key file"
	storeKeyFromCluster = "cluster key"
	storeKeyOff         = "off"
)

// resolveStoreKeys returns the key that encrypts the Snake's message store on disk,
// the previous key when the operator is rotating, and where the key came from. With
// snake.store_encryption off it returns no key.
//
// An operator's key file wins. Without one the key is derived from the cluster key,
// which is why a Mixer encrypts its store with nothing configured.
func resolveStoreKeys(cfg *config.SnakeConfig, cluster *pki.ClusterKey) (key, oldKey, source string, err error) {
	if !cfg.StoreEncryption {
		return "", "", storeKeyOff, nil
	}

	if cfg.StoreKeyFile != "" {
		if key, err = snake.LoadStoreKeyFile(cfg.StoreKeyFile); err != nil {
			return "", "", "", err
		}
		source = storeKeyFromFile
	} else {
		if key, err = cluster.DeriveStoreKey(); err != nil {
			return "", "", "", err
		}
		source = storeKeyFromCluster
	}

	if cfg.StoreOldKeyFile != "" {
		if oldKey, err = snake.LoadStoreKeyFile(cfg.StoreOldKeyFile); err != nil {
			return "", "", "", fmt.Errorf("previous store key: %w", err)
		}
	}
	return key, oldKey, source, nil
}

// parseStreamMaxAge reads snake.stream_max_age. Empty means no limit.
func parseStreamMaxAge(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("snake.stream_max_age %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("snake.stream_max_age %q is negative", s)
	}
	return d, nil
}
