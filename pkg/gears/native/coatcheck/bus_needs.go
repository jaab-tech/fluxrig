// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import "github.com/jaab-tech/fluxrig/pkg/sdk"

// busNeeds names the bucket of this gear. The daemon creates and watches it;
// store and restore read, write and delete its keys.
func busNeeds(cfg map[string]any) []sdk.BusNeed {
	bucket, _ := cfg["bucket"].(string)
	if bucket == "" {
		return nil
	}
	kind := sdk.BusWriteBucket
	if mode, _ := cfg["mode"].(string); mode == ModeDaemon {
		kind = sdk.BusOwnBucket
	}
	return []sdk.BusNeed{{Kind: kind, Bucket: bucket}}
}
