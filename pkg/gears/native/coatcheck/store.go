package coatcheck

import (
	"context"
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/vmihailenco/msgpack/v5"
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
		partial.TsInit = msg.TsInit

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

	// Use vmihailenco msgpack
	valBytes, err := msgpack.Marshal(val)
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
	// ADR says: "Daemon... Schedules timers...".
	// So the Daemon needs to know the specific expiry time.
	// We should probably check if we need to store "ExpireAt" in metadata?
	// Or Daemon calculates it from "TsInit + TTL"? Yes.

	bucket := s.gear.config.Bucket
	_, err = s.gear.ctx.Bus().KV().Put(bucket, key, valBytes)
	if err != nil {
		return nil, fmt.Errorf("coatcheck store: kv put failed: %w", err)
	}

	// 4. Trace/Log
	s.gear.ctx.Logger().Debug("coat stored", "key", key, "bucket", bucket)

	// 5. Forward Original Message
	if s.gear.emit != nil {
		s.gear.emit(msg)
	}
	return nil, nil // Handled manually
}
