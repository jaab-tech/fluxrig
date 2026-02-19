// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/vmihailenco/msgpack/v5"
)

type DaemonLogic struct {
	gear *CoatCheckGear
	sub  bus.Subscription

	mu     sync.Mutex
	timers map[string]*KeyTimer
}

const RehydrationTimeout = 5 * time.Second

// KeyTimer wraps the generic timer
type KeyTimer struct {
	t *time.Timer
}

func (d *DaemonLogic) Init() error {
	d.timers = make(map[string]*KeyTimer)

	// 1. Governance: Create Bucket
	kv := d.gear.ctx.Bus().KV()
	err := kv.EnsureBucket(
		d.gear.config.Bucket,
		d.gear.config.Storage,
		d.gear.config.Replicas,
		d.gear.config.MaxTTL, // Safety Max TTL
	)
	if err != nil {
		return fmt.Errorf("daemon governance failed: %w", err)
	}
	d.gear.ctx.Logger().Info("Daemon Bucket Governance", "bucket", d.gear.config.Bucket, "ttl", d.gear.config.DefaultTTL)
	return nil
}

func (d *DaemonLogic) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	log := d.gear.ctx.Logger()
	bucket := d.gear.config.Bucket

	// 1. Watch Setup via SubscribeRaw (Push-based, Performance optimized)
	// We subscribe to the underlying NATS KV Subject: $KV.<bucket>.>
	// Stream Name for KV is KV_<bucket>
	streamName := fmt.Sprintf("KV_%s", bucket)
	subject := fmt.Sprintf("$KV.%s.>", bucket)
	prefix := fmt.Sprintf("$KV.%s.", bucket)

	sub, err := d.gear.ctx.Bus().SubscribeRaw(subject, streamName, func(ctx context.Context, subj string, data []byte) {
		// Extract Key
		key := strings.TrimPrefix(subj, prefix)
		// d.gear.ctx.Logger().Info("Daemon Raw Event", "key", key, "len", len(data))

		// Heuristic: Non-empty data = PUT, Empty data = DEL/PURGE
		// (Safe for CoatCheck because we always store MsgPack blobs, never empty bytes)
		if len(data) > 0 {
			d.scheduleExpiry(key, data, emit)
		} else {
			d.cancelExpiry(key)
		}
	})
	if err != nil {
		return fmt.Errorf("daemon raw subscribe failed: %w", err)
	}

	d.sub = sub
	log.Info("daemon watching bucket", "bucket", bucket, "method", "SubscribeRaw")

	return nil
}

func (d *DaemonLogic) Stop() {
	if d.sub != nil {
		_ = d.sub.Unsubscribe()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for k, t := range d.timers {
		t.t.Stop()
		delete(d.timers, k)
	}
}

func (d *DaemonLogic) scheduleExpiry(key string, value []byte, emit func(*fluxmsg.FluxMsg)) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// If timer exists, stop it (update)
	if old, exists := d.timers[key]; exists {
		old.t.Stop()
		delete(d.timers, key)
	}

	// Calculate Timeout Duration
	ttl := d.gear.config.DefaultTTL

	// Optimization: Partial Decode to check for Override
	if d.gear.config.IncludeValues {
		var msg fluxmsg.FluxMsg
		// We ignore error here to allow robust governance (fallback to default TTL)
		if err := msgpack.Unmarshal(value, &msg); err == nil {
			// Check Metadata override
			if val, ok := msg.Metadata[fluxmsg.MetaCoatCheckTTL]; ok {
				if parsed, parseErr := time.ParseDuration(val); parseErr == nil {
					ttl = parsed
				} else {
					d.gear.ctx.Logger().Warn("ttl override parse failed", "key", key, "val", val, "error", parseErr)
				}
			}
		} else {
			d.gear.ctx.Logger().Warn("daemon unmarshal failed", "key", key, "error", err)
		}
	}

	// SAFETY CAP: Enforce MaxTTL to avoid long-lived keys if users request excessive TTLs
	// The bucket itself is backed by MaxTTL in NATS (EnsureBucket), so keys naturally expire there.
	// But we also cap the application timer to ensure we emit the event.
	if ttl > d.gear.config.MaxTTL {
		d.gear.ctx.Logger().Warn("ttl capped by safety limit", "key", key, "requested", ttl, "limit", d.gear.config.MaxTTL)
		ttl = d.gear.config.MaxTTL
	}

	// Set Timer
	timer := time.AfterFunc(ttl, func() {
		d.handleTimeout(key, value, emit)
	})

	d.timers[key] = &KeyTimer{t: timer}
}

func (d *DaemonLogic) cancelExpiry(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, exists := d.timers[key]; exists {
		t.t.Stop()
		delete(d.timers, key)
	}
}

func (d *DaemonLogic) handleTimeout(key string, value []byte, emit func(*fluxmsg.FluxMsg)) {
	d.mu.Lock()
	// Clean up timer map
	delete(d.timers, key)
	d.mu.Unlock()

	log := d.gear.ctx.Logger()
	log.Info("coat expired", "key", key)

	// 1. Emit Timeout Event (to Bus)
	// We use the 'emit' function passed in Start (which emits to OUT port of Daemon).
	// ADR says Daemon is Source.
	// We wrap the expired coat in a new FluxMsg? Or emit the original?
	// ADR 0029 section 4: "Payload Config: include_values: true ensures event contains Context Blob".

	// If we can unmarshal, we emit the original enriched with error?
	// Or we create a new Event Msg.
	event := fluxmsg.New()
	event.Metadata["event.type"] = "flux.event.timeout" // Standardize
	event.Metadata["timeout.key"] = key
	event.Metadata["timeout.bucket"] = d.gear.config.Bucket

	if d.gear.config.IncludeValues {
		// Attach original as payload or raw?
		// Trying to unmarshal original
		var orig fluxmsg.FluxMsg
		if err := msgpack.Unmarshal(value, &orig); err == nil {
			// Merge important fields or attach as data
			event.Data["expired_ctx"] = orig.Data // or raw?
			event.Metadata["expired.src_id"] = fmt.Sprint(orig.SrcGearID)
		} else {
			event.RawPayload = value
		}
	}

	emit(event)

	// 2. Delete from KV ensures it's gone
	// Wait, if we delete, the Watcher triggers "DEL", which calls cancelExpiry.
	// Does "DEL" cause loops?
	// DEL -> cancelExpiry (Stop timer) -> Timer already fired. Safe.

	bucket := d.gear.config.Bucket
	_ = d.gear.ctx.Bus().KV().Delete(bucket, key)
}
