// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// errNoBus is returned by a busRef that holds no bus.
var errNoBus = errors.New("no bus")

// busRef is the bus the gears of a Rack hold. It forwards to the bus the Rack
// currently has, which can be replaced: a Rack that started without the Mixer runs
// its gears on a bus that never connected, and when the Mixer returns the gears must
// use the new connection without being started again. Handing them this reference
// rather than the bus itself is what makes that possible.
type busRef struct {
	mu sync.RWMutex
	b  bus.Bus
}

func newBusRef(b bus.Bus) *busRef { return &busRef{b: b} }

func (r *busRef) current() bus.Bus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.b
}

// set replaces the bus the reference forwards to.
func (r *busRef) set(b bus.Bus) {
	r.mu.Lock()
	r.b = b
	r.mu.Unlock()
}

func (r *busRef) Connect(url string, opts bus.ConnectOptions) error {
	b := r.current()
	if b == nil {
		return errNoBus
	}
	return b.Connect(url, opts)
}

func (r *busRef) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	b := r.current()
	if b == nil {
		return errNoBus
	}
	return b.Publish(ctx, subject, msg)
}

func (r *busRef) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	b := r.current()
	if b == nil {
		return errNoBus
	}
	return b.PublishRaw(ctx, subject, data, fluxID)
}

func (r *busRef) Subscribe(subject string, handler bus.Handler) (bus.Subscription, error) {
	b := r.current()
	if b == nil {
		return nil, errNoBus
	}
	return b.Subscribe(subject, handler)
}

func (r *busRef) SubscribeRaw(subject, streamName string, handler bus.RawHandler) (bus.Subscription, error) {
	b := r.current()
	if b == nil {
		return nil, errNoBus
	}
	return b.SubscribeRaw(subject, streamName, handler)
}

func (r *busRef) SubscribeDurable(subject, durableName string, handler bus.Handler) (bus.Subscription, error) {
	b := r.current()
	if b == nil {
		return nil, errNoBus
	}
	return b.SubscribeDurable(subject, durableName, handler)
}

func (r *busRef) KV() bus.KeyValue {
	if b := r.current(); b != nil {
		return b.KV()
	}
	return nil
}

func (r *busRef) Core() any {
	if b := r.current(); b != nil {
		return b.Core()
	}
	return nil
}

// Close closes the bus the reference holds now.
func (r *busRef) Close() {
	if b := r.current(); b != nil {
		b.Close()
	}
}

// Purge empties the stream of a bus that can, and does nothing for one that cannot.
func (r *busRef) Purge(ctx context.Context) error {
	if p, ok := r.current().(interface{ Purge(context.Context) error }); ok {
		return p.Purge(ctx)
	}
	return nil
}

var _ bus.Bus = (*busRef)(nil)
