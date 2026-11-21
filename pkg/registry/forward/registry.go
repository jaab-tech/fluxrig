// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Holder answers whether this Mixer currently holds the write lease.
type Holder interface {
	Held() bool
}

// Registry is a registry.Registry that forwards a control write to the lease
// holder when this Mixer does not hold it, and delegates locally when it
// does. Reads always delegate locally: every Mixer serves reads from its own
// copy, and only writes need the one holder.
//
// With a nil holder the decorator is pass-through, so a single Mixer is
// unaffected.
type Registry struct {
	local  registry.Registry
	fwd    *Forwarder
	holder Holder
}

// NewRegistry wraps local. holder nil means pass-through.
func NewRegistry(local registry.Registry, fwd *Forwarder, holder Holder) *Registry {
	return &Registry{local: local, fwd: fwd, holder: holder}
}

func (r *Registry) forwarding() bool {
	return r.fwd != nil && r.holder != nil && !r.holder.Held()
}

var _ registry.Registry = (*Registry)(nil)

func (r *Registry) Register(ctx context.Context, machineID uuid.UUID, name, secret, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	return r.RegisterEntity(ctx, 4, machineID, name, secret, ip, port, version, config, nil, mixerID)
}

func (r *Registry) RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name, secret, ip string, port int, version string, config, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	if !r.forwarding() {
		return r.local.RegisterEntity(ctx, typeID, machineID, name, secret, ip, port, version, config, attrs, mixerID)
	}
	resp, err := r.fwd.Do(ctx, Request{
		Op: OpRegisterEntity, TypeID: typeID, MachineID: machineID, Name: name, Secret: secret,
		IP: ip, Port: port, Version: version, Config: config, Attrs: attrs, MixerID: mixerID,
	})
	if err != nil {
		return nil, err
	}
	if err := ErrorOf(resp.ErrorCode, resp.ErrorText); err != nil {
		return nil, err
	}
	if resp.Rack != nil {
		resp.Rack.Secret = resp.IssuedSecret
	}
	return resp.Rack, nil
}

func (r *Registry) Approve(ctx context.Context, id uuid.UUID, newName string) (*registry.Rack, error) {
	if !r.forwarding() {
		return r.local.Approve(ctx, id, newName)
	}
	resp, err := r.fwd.Do(ctx, Request{Op: OpApprove, ID: id, NewName: newName})
	if err != nil {
		return nil, err
	}
	if err := ErrorOf(resp.ErrorCode, resp.ErrorText); err != nil {
		return nil, err
	}
	return resp.Rack, nil
}

func (r *Registry) RemoveEntity(ctx context.Context, typeID uint8, id uuid.UUID) error {
	if !r.forwarding() {
		return r.local.RemoveEntity(ctx, typeID, id)
	}
	resp, err := r.fwd.Do(ctx, Request{Op: OpRemoveEntity, TypeID: typeID, ID: id})
	if err != nil {
		return err
	}
	return ErrorOf(resp.ErrorCode, resp.ErrorText)
}

func (r *Registry) Remove(ctx context.Context, id uuid.UUID) error {
	return r.RemoveEntity(ctx, 4, id)
}

func (r *Registry) RemoveByName(ctx context.Context, name string) error {
	if !r.forwarding() {
		return r.local.RemoveByName(ctx, name)
	}
	resp, err := r.fwd.Do(ctx, Request{Op: OpRemoveByName, Name: name})
	if err != nil {
		return err
	}
	return ErrorOf(resp.ErrorCode, resp.ErrorText)
}

func (r *Registry) UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error {
	if !r.forwarding() {
		return r.local.UpdateStatusEntity(ctx, typeID, id, status)
	}
	resp, err := r.fwd.Do(ctx, Request{Op: OpUpdateStatusEntity, TypeID: typeID, ID: id, Status: status})
	if err != nil {
		return err
	}
	return ErrorOf(resp.ErrorCode, resp.ErrorText)
}

func (r *Registry) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.UpdateStatusEntity(ctx, 4, id, status)
}

// Reads and presence delegate locally. They are not forwarded: every Mixer
// serves them from its own copy, and a heartbeat is presence, not a control
// write.

func (r *Registry) GetRackByName(ctx context.Context, name string) (uuid.UUID, error) {
	// A read: local, like every other read. The caller uses it to resolve a
	// deploy target, which each Mixer's own copy answers.
	if looker, ok := r.local.(interface {
		GetRackByName(context.Context, string) (uuid.UUID, error)
	}); ok {
		return looker.GetRackByName(ctx, name)
	}
	return uuid.Nil, registry.ErrNotFound
}

func (r *Registry) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	return r.local.List(ctx, status)
}
func (r *Registry) Get(ctx context.Context, id uuid.UUID) (*registry.Rack, error) {
	return r.local.Get(ctx, id)
}
func (r *Registry) Heartbeat(ctx context.Context, id uuid.UUID, stats, config map[string]any) error {
	return r.local.Heartbeat(ctx, id, stats, config)
}
func (r *Registry) HeartbeatEntity(ctx context.Context, typeID uint8, id uuid.UUID, stats, config map[string]any) error {
	return r.local.HeartbeatEntity(ctx, typeID, id, stats, config)
}
func (r *Registry) SetAutoAdopt(enabled bool)        { r.local.SetAutoAdopt(enabled) }
func (r *Registry) SetBootstrapSecret(secret string) { r.local.SetBootstrapSecret(secret) }
func (r *Registry) QueryLogs(ctx context.Context, q registry.LogQuery) ([]registry.LogEntry, error) {
	return r.local.QueryLogs(ctx, q)
}
func (r *Registry) QueryMetrics(ctx context.Context, q registry.MetricQuery) ([]registry.MetricEntry, error) {
	return r.local.QueryMetrics(ctx, q)
}
func (r *Registry) ClearScenarioEntities(ctx context.Context) error {
	return r.local.ClearScenarioEntities(ctx)
}
