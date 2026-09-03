// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

type StoreLogic struct {
	gear *CoatCheckGear
}

func (s *StoreLogic) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	// 1. Extract Correlation Key
	key, err := s.gear.extractKey(msg)
	if err != nil {
		return nil, fmt.Errorf("coatcheck store: key extraction failed: %w", err)
	}

	// 2. Prepare Value (The "Coat")
	var val interface{} = msg

	if len(s.gear.config.ValueFields) > 0 {
		// Partial Storage: Create a lightweight FluxMsg clone
		partial := fluxmsg.New()
		partial.FluxID = msg.FluxID // Keep ID for reference
		partial.TSInit = msg.TSInit

		for _, field := range s.gear.config.ValueFields {
			extracted, found := sdk.GetValue(msg, field)
			if found {
				// Inject back into partial message
				// Note: Currently sdk.SetValue is not public, so we manually handle common cases
				// For now, we only support Metadata filtering for partial storage as per use case
				if len(field) > 5 && field[:5] == "meta." {
					metaKey := field[5:]
					if strVal, ok := extracted.(string); ok {
						partial.Metadata[metaKey] = strVal
					}
				}
				// TODO: Support deeper data setting if needed
			}
		}
		val = partial
	}

	// Use CBOR
	valBytes, err := cbor.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("coatcheck store: serialize failed: %w", err)
	}

	// 3. Store in KV
	// Do we have a TTL override in the message?
	// fluxmsg.MetaCoatCheckTTL

	// KV Put.
	// Note: NATS KV TTL is usually bucket-wide. Per-key TTL is not supported in basic KV logic
	// UNLESS using 'Purge' or external cleaner (which is our Daemon!).
	// So we just Put. The Daemon will read the expiration from the value or default.
	// Wait, we need to KNOW when it expires.
	// Strategy: We can't rely on NATS to auto-expire individual keys if bucket has MAX_AGE.
	// But if bucket has TTL, ALL keys expire.
	// The specification says: "Daemon... Schedules timers...".
	// So the Daemon needs to know the specific expiry time.
	// We should probably check if we need to store "ExpireAt" in metadata?
	// Or Daemon calculates it from "TSInit + TTL"? Yes.

	bucket := s.gear.config.Bucket

	// The store is a write to the bus, so it costs a round trip. Whether the
	// message waits for it is a property of what the entry is for.
	//
	// Blocking is right when the reply depends on the entry: stripping a PAN
	// and reattaching it later is meaningless if the strip is forwarded and the
	// store then fails, because the reply can never be made whole. Failing the
	// message is the honest outcome there.
	//
	// It is wrong when the entry only enriches a record. A stamp parked to
	// measure a round trip is worth losing: holding an authorization, or worse
	// failing it, because a control-plane write was slow trades a payment for a
	// metric.
	if !s.gear.config.AwaitStore {
		go func() {
			// Detached from the message's context, which is cancelled as soon as
			// the message moves on; inheriting it would cancel nearly every write.
			putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.gear.config.StoreTimeout)
			defer cancel()
			if _, putErr := s.gear.ctx.Bus().KV().Put(putCtx, bucket, key, valBytes); putErr != nil {
				s.gear.ctx.Logger().Warn("coat store failed, message already forwarded",
					"key", key, "bucket", bucket, "error", putErr)
			}
		}()
		if s.gear.emit != nil {
			s.gear.emit(msg)
		}
		return nil, nil
	}

	if _, err = s.gear.ctx.Bus().KV().Put(ctx, bucket, key, valBytes); err != nil {
		return nil, fmt.Errorf("coatcheck store: kv put failed: %w", err)
	}

	s.gear.ctx.Logger().Debug("coat stored", "key", key, "bucket", bucket)

	if s.gear.emit != nil {
		s.gear.emit(msg)
	}
	return nil, nil // Handled manually
}
