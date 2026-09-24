// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go"
)

// ErrControlPlaneOffline is returned by Publish while no connection is bound and no
// gear of this Rack is listening for the command.
var ErrControlPlaneOffline = errors.New("control plane is offline: no bus connection and no local listener")

// LateControlPlane is a ControlPlane whose connection can come after the gears that
// use it. A Rack that starts without the Mixer runs its gears before it has a bus.
// Those gears subscribe to their commands when they start; the subscriptions are
// kept and attached when the connection is bound, so the Mixer's commands reach
// them once it is back without the gears being started again.
//
// While no connection is bound, a command published to a gear of this Rack is
// delivered in memory, which is what keeps the gears of a Rack that runs without
// the Mixer able to signal each other (a link that goes down, a connection to
// close). With a connection, every command goes over it as before.
type LateControlPlane struct {
	mu   sync.Mutex
	nc   *nats.Conn
	subs []*lateSub
}

type lateSub struct {
	gearID string
	out    chan Command
	sub    *nats.Subscription // nil until it is attached to a connection
}

// NewLateControlPlane returns a control plane with no connection.
func NewLateControlPlane() *LateControlPlane { return &LateControlPlane{} }

// Bind gives the control plane its connection and attaches every subscription made
// so far to it. Binding the connection it already has attaches only what is not
// attached yet, so a subscription that failed to attach is tried again; a nil
// connection is ignored. A different connection replaces the one before it, and the
// subscriptions move to it.
func (cp *LateControlPlane) Bind(nc *nats.Conn) {
	if nc == nil {
		return
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.nc != nc {
		cp.nc = nc
		for _, s := range cp.subs {
			if s.sub != nil {
				_ = s.sub.Unsubscribe()
				s.sub = nil
			}
		}
	}
	for _, s := range cp.subs {
		if s.sub != nil {
			continue
		}
		if err := cp.attach(s); err != nil {
			slog.Error("control plane: cannot attach a subscription", "gear", s.gearID, "error", err)
		}
	}
}

// Bound reports whether a connection has been bound.
func (cp *LateControlPlane) Bound() bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.nc != nil
}

// attach subscribes s on the bound connection. The caller holds mu.
func (cp *LateControlPlane) attach(s *lateSub) error {
	sub, err := subscribeInto(cp.nc, s.gearID, s.out)
	if err != nil {
		return err
	}
	s.sub = sub
	return nil
}

// Release ends every subscription a gear made, and forgets it. The Manager calls it
// when it stops a gear: without it a scenario applied again would leave the earlier
// gear's subscription in place, listening for commands that nobody reads. It is safe
// to call for a gear that has none.
func (cp *LateControlPlane) Release(gearID string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	kept := cp.subs[:0]
	for _, s := range cp.subs {
		if s.gearID != gearID {
			kept = append(kept, s)
			continue
		}
		if s.sub != nil {
			_ = s.sub.Unsubscribe()
		}
	}
	// Clear the tail so the released subscriptions can be collected.
	for i := len(kept); i < len(cp.subs); i++ {
		cp.subs[i] = nil
	}
	cp.subs = kept
}

// Publish sends a command to a gear. Without a connection it goes to the gears of
// this Rack that subscribed to it, and fails with ErrControlPlaneOffline if there
// are none.
func (cp *LateControlPlane) Publish(targetGearID string, cmd Command) error {
	cp.mu.Lock()
	nc := cp.nc
	var local []*lateSub
	if nc == nil {
		for _, s := range cp.subs {
			if s.gearID == targetGearID {
				local = append(local, s)
			}
		}
	}
	cp.mu.Unlock()

	if nc != nil {
		return NewNATSControlPlane(nc).Publish(targetGearID, cmd)
	}
	if len(local) == 0 {
		return ErrControlPlaneOffline
	}
	for _, s := range local {
		select {
		case s.out <- cmd:
		default:
			slog.Warn("control plane: a listener's queue is full, command dropped", "gear", s.gearID, "cmd", cmd.Cmd)
		}
	}
	return nil
}

// Subscribe returns the channel the commands for myGearID arrive on. It never fails
// for want of a connection: the subscription is attached when one is bound. With a
// connection bound, it fails if the subscription cannot be made, so that a gear that
// waits for a command does not wait for one it cannot hear.
func (cp *LateControlPlane) Subscribe(myGearID string) (<-chan Command, error) {
	s := &lateSub{gearID: myGearID, out: make(chan Command, 100)}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.nc != nil {
		if err := cp.attach(s); err != nil {
			return nil, err
		}
	}
	cp.subs = append(cp.subs, s)
	return s.out, nil
}

var _ ControlPlane = (*LateControlPlane)(nil)
