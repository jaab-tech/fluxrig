// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wasm

import (
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// busNeeds names the bucket a module is read from, for a source of the form
// snake://<bucket>/<key>.
func busNeeds(cfg map[string]any) []sdk.BusNeed {
	src, _ := cfg["source"].(string)
	rest, ok := strings.CutPrefix(src, "snake://")
	if !ok {
		return nil
	}
	bucket, _, ok := strings.Cut(rest, "/")
	if !ok || bucket == "" {
		return nil
	}
	return []sdk.BusNeed{{Kind: sdk.BusReadBucket, Bucket: bucket}}
}
