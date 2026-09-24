// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"
	"strings"

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
			if !found {
				continue
			}
			// Inject back into the partial message. Meta paths land
			// in Metadata (strings only); every other path nests into
			// Data via FluxMsg.Set, which builds the maps as needed.
			if len(field) > 5 && field[:5] == "meta." {
				if strVal, ok := extracted.(string); ok {
					partial.Metadata[field[5:]] = strVal
				}
				continue
			}
			if strings.HasPrefix(field, "data.") {
				_ = partial.Set(field[5:], extracted)
				continue
			}
			_ = partial.Set(field, extracted)
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
	// Shield: remove the stored fields from the forwarded message.
	// This is the coat check itself: downstream travels on the ticket
	// (the correlation key), not the coat. The blocking path strips
	// only after the put succeeds. Fire-and-forget strips on dispatch:
	// if that write then fails, the value is gone (the documented cost
	// of await_store:false; never use it for PANs).
	strip := func() {
		for _, field := range s.gear.config.ValueFields {
			deleteDotted(msg, field)
		}
	}

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
		strip()
		if s.gear.emit != nil {
			s.gear.emit(msg)
		}
		return nil, nil
	}

	if _, err = s.gear.ctx.Bus().KV().Put(ctx, bucket, key, valBytes); err != nil {
		return nil, fmt.Errorf("coatcheck store: kv put failed: %w", err)
	}
	strip()

	s.gear.ctx.Logger().Debug("coat stored", "key", key, "bucket", bucket)

	if s.gear.emit != nil {
		s.gear.emit(msg)
	}
	return nil, nil // Handled manually
}

// deleteDotted removes one configured path from the forwarded message:
// meta.* paths from Metadata, data.* paths (prefix stripped) and bare
// paths from nested Data. Both map shapes (in-process and post-CBOR)
// are handled, mirroring the read side.
func deleteDotted(msg *fluxmsg.FluxMsg, path string) {
	if msg == nil {
		return
	}
	if len(path) > 5 && path[:5] == "meta." {
		if msg.Metadata != nil {
			delete(msg.Metadata, path[5:])
		}
		return
	}
	parts := strings.Split(path, ".")
	if len(parts) > 1 && parts[0] == "data" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return
	}
	deleteNested(msg.Data, parts)
}

func deleteNested(root map[string]any, parts []string) bool {
	if root == nil || len(parts) == 0 {
		return false
	}
	if len(parts) == 1 {
		if _, ok := root[parts[0]]; ok {
			delete(root, parts[0])
			return true
		}
		return false
	}
	next, ok := root[parts[0]]
	if !ok {
		return false
	}
	if _, alreadyStringMap := next.(map[string]any); !alreadyStringMap {
		// A CBOR-round-tripped map[any]any: normalized once, in the one place
		// this shape is handled across the codebase (fluxmsg.AsDataMap), and
		// written back so descending into it (and the caller's own copy)
		// agree on what root[parts[0]] now holds.
		converted, ok := fluxmsg.AsDataMap(next)
		if !ok {
			return false
		}
		root[parts[0]] = converted
		next = converted
	}
	m := next.(map[string]any)
	removed := deleteNested(m, parts[1:])
	if removed && len(m) == 0 {
		delete(root, parts[0])
	}
	return removed
}
