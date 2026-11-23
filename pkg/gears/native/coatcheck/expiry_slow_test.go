// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
)

type slowPutKV struct {
	bus.KeyValue
	delay time.Duration
}

func (k slowPutKV) Follow(ctx context.Context, bucket string, h func(bus.KVRecord)) (bus.Subscription, error) {
	return k.KeyValue.Follow(ctx, bucket, func(rec bus.KVRecord) {
		if rec.Op == bus.KVPut {
			time.Sleep(k.delay)
		}
		h(rec)
	})
}
