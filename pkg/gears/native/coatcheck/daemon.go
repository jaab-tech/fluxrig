// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// DaemonLogic sends one timeout event for each entry that it saw parked and
// that expires.
//
// An entry has two clocks: the TTL that the server applies and the timer of
// the daemon. Whichever ends first is the expiry. The timer runs for the
// whole TTL from when the daemon saw the entry, so that a clock of the Rack
// that differs from the server's cannot expire it early. For an entry that
// was parked before the daemon started, the server's marker comes first.
type DaemonLogic struct {
	gear *CoatCheckGear
	sub  bus.Subscription

	mu      sync.Mutex
	timers  map[string]*KeyTimer
	stopped bool
}

const RehydrationTimeout = 5 * time.Second

// KeyTimer is what the daemon knows of the latest record of one key.
type KeyTimer struct {
	t     *time.Timer // nil for a deleted key
	value []byte
	// noEvent says that a KVExpired for the key is not a timeout to send: the
	// timer sent it already, or it is the tombstone of a delete ageing out.
	noEvent bool
}

func (d *DaemonLogic) Init() error {
	d.timers = make(map[string]*KeyTimer)

	// 1. Governance: Create Bucket
	kv := d.gear.ctx.Bus().KV()
	err := kv.EnsureBucket(
		context.Background(),
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

	d.mu.Lock()
	d.stopped = false
	d.mu.Unlock()

	// Follow starts with what the bucket holds: a scenario activation
	// restarts the gears while entries are parked.
	sub, err := d.gear.ctx.Bus().KV().Follow(ctx, bucket, func(rec bus.KVRecord) {
		d.observe(rec, emit)
	})
	if err != nil {
		return fmt.Errorf("daemon follow failed: %w", err)
	}

	d.sub = sub
	log.Info("daemon watching bucket", "bucket", bucket, "method", "Follow")

	return nil
}

func (d *DaemonLogic) Stop() {
	if d.sub != nil {
		_ = d.sub.Unsubscribe()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for k, t := range d.timers {
		t.stop()
		delete(d.timers, k)
	}
}

func (k *KeyTimer) stop() {
	if k.t != nil {
		k.t.Stop()
	}
}

// observe applies one record of the bucket.
func (d *DaemonLogic) observe(rec bus.KVRecord, emit func(*fluxmsg.FluxMsg)) {
	switch rec.Op {
	case bus.KVPut:
		d.scheduleExpiry(rec.Key, rec.Value, rec.Revision, emit)
	case bus.KVPurged:
		d.cancelExpiry(rec.Key)
	case bus.KVDeleted:
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.stopped {
			return
		}
		if old, exists := d.timers[rec.Key]; exists {
			old.stop()
		}
		d.timers[rec.Key] = &KeyTimer{noEvent: true}
	case bus.KVExpired:
		d.expiredByServer(rec, emit)
	}
}

// expiredByServer sends the timeout of an entry that the server removed
// before the timer fired. The marker of a key that this daemon did not see is
// not a timeout: that of an entry which expired while no daemon ran is the
// same as that of a deleted entry whose tombstone aged out.
func (d *DaemonLogic) expiredByServer(rec bus.KVRecord, emit func(*fluxmsg.FluxMsg)) {
	d.mu.Lock()
	known, seen := d.timers[rec.Key]
	if d.stopped || !seen {
		d.mu.Unlock()
		return
	}
	known.stop()
	send, value := !known.noEvent, known.value
	delete(d.timers, rec.Key)
	d.mu.Unlock()

	// Off the consumer's goroutine: the purge is a round trip to the bus.
	go func() {
		if send {
			d.handleTimeout(rec.Key, value, rec.Revision, emit)
			return
		}
		d.purge(rec.Key, rec.Revision)
	}()
}

func (d *DaemonLogic) scheduleExpiry(key string, value []byte, revision uint64, emit func(*fluxmsg.FluxMsg)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}

	// If timer exists, stop it (update)
	if old, exists := d.timers[key]; exists {
		old.stop()
		delete(d.timers, key)
	}

	// Calculate Timeout Duration. The per-message override must be honored
	// regardless of include_values: that setting controls only whether
	// handleTimeout's emitted event carries the full original value, a
	// separate concern from whether this entry's own expiry respects what the
	// scenario asked for. The same resolver the store uses sets the KV entry's
	// own storage TTL, so the timer and the storage expiry agree.
	var msg fluxmsg.FluxMsg
	// A decode failure falls back to the default TTL, which resolveTTL applies
	// from a nil message; governance stays robust rather than failing here.
	if err := cbor.Unmarshal(value, &msg); err != nil {
		d.gear.ctx.Logger().Warn("daemon unmarshal failed", "key", key, "error", err)
		msg = fluxmsg.FluxMsg{}
	}
	ttl := d.gear.resolveTTL(&msg, d.gear.config.DefaultTTL, d.gear.config.MaxTTL)

	// Set Timer
	entry := &KeyTimer{value: value}
	entry.t = time.AfterFunc(ttl, func() {
		d.mu.Lock()
		// A timer that already fired cannot be stopped: a newer record of
		// the key, or the server's marker, may have taken its place.
		if d.stopped || d.timers[key] != entry {
			d.mu.Unlock()
			return
		}
		entry.noEvent = true
		entry.value = nil
		d.mu.Unlock()
		d.handleTimeout(key, value, revision, emit)
	})

	d.timers[key] = entry
}

func (d *DaemonLogic) cancelExpiry(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, exists := d.timers[key]; exists {
		t.stop()
		delete(d.timers, key)
	}
}

// handleTimeout sends the timeout event of one revision of a key and purges
// that revision.
func (d *DaemonLogic) handleTimeout(key string, value []byte, revision uint64, emit func(*fluxmsg.FluxMsg)) {
	log := d.gear.ctx.Logger()
	log.Info("coat expired", "key", key)

	// 1. Emit Timeout Event (to Bus)
	// We use the 'emit' function passed in Start (which emits to OUT port of Daemon).
	// The specification says Daemon is Source.
	// We wrap the expired coat in a new FluxMsg? Or emit the original?
	// The specification section 4: "Payload Config: include_values: true ensures event contains Context Blob".

	// If we can unmarshal, we emit the original enriched with error?
	// Or we create a new Event Msg.
	event := fluxmsg.New()
	event.Metadata["event.type"] = "flux.event.timeout" // Standardize
	event.Metadata["timeout.key"] = key
	event.Metadata["timeout.bucket"] = d.gear.config.Bucket

	if d.gear.config.IncludeValues && len(value) > 0 {
		// Attach original as payload or raw?
		// Trying to unmarshal original
		var orig fluxmsg.FluxMsg
		if err := cbor.Unmarshal(value, &orig); err == nil {
			// Merge important fields or attach as data
			event.Data["expired_ctx"] = orig.Data // or raw?
			event.Metadata["expired.src_id"] = fmt.Sprint(orig.SrcGearID)
		} else {
			event.RawPayload = value
		}
	}

	emit(event)

	// 2. Remove what expired
	d.purge(key, revision)
}

// purge removes one revision of a key. A key parked again since has a newer
// one and stays. A purge, not a delete: the server follows the tombstone of a
// delete with an expiry marker, which would be a second timeout.
func (d *DaemonLogic) purge(key string, revision uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), d.gear.config.StoreTimeout)
	defer cancel()
	if err := d.gear.ctx.Bus().KV().PurgeRevision(ctx, d.gear.config.Bucket, key, revision); err != nil {
		d.gear.ctx.Logger().Debug("expired coat not purged", "key", key, "revision", revision, "error", err)
	}
}
