// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/vmihailenco/msgpack/v5"
)

type RestoreLogic struct {
	gear *CoatCheckGear
}

func (r *RestoreLogic) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	// 1. Extract Correlation Key (Same logic as Store)
	key, err := r.gear.extractKey(msg)
	if err != nil {
		return nil, fmt.Errorf("coatcheck restore: key extraction failed: %w", err)
	}

	// 2. Fetch from KV
	bucket := r.gear.config.Bucket
	valBytes, _, err := r.gear.ctx.Bus().KV().Get(bucket, key)
	if err != nil {
		// Log error but check on_missing policy
		return r.handleMissing(ctx, msg, key, err)
	}
	if valBytes == nil {
		// Key not found (Expired or never stored)
		return r.handleMissing(ctx, msg, key, nil)
	}

	// 3. Decode Context Blob (The "Coat")
	// We expect a FluxMsg envelope.
	var savedMsg fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(valBytes, &savedMsg); err != nil {
		return nil, fmt.Errorf("coatcheck restore: decode failed: %w", err)
	}

	// 4. Merge Context (Enrichment)
	// We preserve the *current* msg Payload (it's the Response).
	// We inject the *saved* Metadata and Tracing Context.

	// A. Metadata Merge (Sensitivity to MergeStrategy)
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}

	shouldOverwrite := r.gear.config.MergeStrategy == "overwrite"

	var restoredKeys []string
	for k, v := range savedMsg.Metadata {
		// If overwrite is ON, we set it regardless.
		// If overwrite is OFF (preserve), we only set if !exists.
		if _, exists := msg.Metadata[k]; shouldOverwrite || !exists {
			msg.Metadata[k] = v
			restoredKeys = append(restoredKeys, k)
		}
	}

	// B. Identity Restoration (Optional)
	// If we want to restore the original FluxID as RefID?
	// msg.RefFluxID = savedMsg.FluxID (This links Response to Request!)
	if msg.RefFluxID == 0 {
		msg.RefFluxID = savedMsg.FluxID
	}

	// C. Restore TraceID if missing
	if msg.TraceID == "" {
		msg.TraceID = savedMsg.TraceID
	}

	// D. Restore Saved Data fields? (e.g. original PAN?)
	// Configurable key_fields might need this?
	// For now, only Metadata is merged.

	r.gear.ctx.Logger().Debug("coat restored", "key", key, "restored_keys", fmt.Sprintf("%v", restoredKeys))

	// 5. Cleanup?
	// Do we delete the key after restore? "Claim Check" implies getting item back.
	// But in some flows, we might want multiple restores (broadcast)?
	// ADR 0029 doesn't specify "Delete on Restore".
	// Usually Coat Check is one-time use.
	// Let's assume one-time use requires an explicit "delete_on_restore" config?
	// Default: Keep it until TTL (Safer for retries).

	if r.gear.emit != nil {
		r.gear.emit(msg)
	}
	return nil, nil
}

func (r *RestoreLogic) handleMissing(ctx context.Context, msg *fluxmsg.FluxMsg, key string, reason error) (*fluxmsg.FluxMsg, error) {
	log := r.gear.ctx.Logger()
	log.Debug("coat missing", "key", key, "reason", reason)

	switch r.gear.config.OnMissing {
	case "error":
		return nil, fmt.Errorf("coatcheck restore: missing context for key %s", key)
	case "drop":
		return nil, nil // Silently drop
	case "forward":
		fallthrough
	default:
		// Pass through without context
		if r.gear.emit != nil {
			r.gear.emit(msg)
		}
		return nil, nil
	}
}
