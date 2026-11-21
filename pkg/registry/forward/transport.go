// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// NATSTransport adapts a core NATS connection to the Transport interface.
type NATSTransport struct {
	nc *nats.Conn
}

// NewNATSTransport wraps nc. A nil connection yields nil, so a single Mixer
// that forwards nothing gets a nil forwarder.
func NewNATSTransport(nc *nats.Conn) *NATSTransport {
	if nc == nil {
		return nil
	}
	return &NATSTransport{nc: nc}
}

// Request sends data on subject and returns the reply.
func (t *NATSTransport) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	if t == nil || t.nc == nil {
		return nil, fmt.Errorf("forward: no NATS connection")
	}
	msg, err := t.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// ScenarioRepo is a scenario store whose control writes (put a scenario,
// move the active pointer) forward to the lease holder, while reads stay
// local. It mirrors the registry decorator for the scenario side.
type ScenarioRepo interface {
	PutScenario(ctx context.Context, name string, content []byte) error
	SetActive(ctx context.Context, active registry.ActiveScenario) error
	GetActive(ctx context.Context) (registry.ActiveScenario, error)
	GetScenario(ctx context.Context, name string) ([]byte, error)
	ListScenarios(ctx context.Context) ([]string, error)
}

// Scenario wraps a ScenarioRepo, forwarding its two writes.
type Scenario struct {
	local  ScenarioRepo
	fwd    *Forwarder
	holder Holder
}

// NewScenario wraps local. holder nil means pass-through.
func NewScenario(local ScenarioRepo, fwd *Forwarder, holder Holder) *Scenario {
	return &Scenario{local: local, fwd: fwd, holder: holder}
}

func (s *Scenario) forwarding() bool {
	return s.fwd != nil && s.holder != nil && !s.holder.Held()
}

func (s *Scenario) PutScenario(ctx context.Context, name string, content []byte) error {
	if !s.forwarding() {
		return s.local.PutScenario(ctx, name, content)
	}
	resp, err := s.fwd.Do(ctx, Request{Op: OpPutScenario, ScenarioName: name, ScenarioContent: content})
	if err != nil {
		return err
	}
	return ErrorOf(resp.ErrorCode, resp.ErrorText)
}

func (s *Scenario) SetActive(ctx context.Context, active registry.ActiveScenario) error {
	if !s.forwarding() {
		return s.local.SetActive(ctx, active)
	}
	resp, err := s.fwd.Do(ctx, Request{Op: OpSetActive, Active: &active})
	if err != nil {
		return err
	}
	return ErrorOf(resp.ErrorCode, resp.ErrorText)
}

// Reads delegate locally.
func (s *Scenario) GetActive(ctx context.Context) (registry.ActiveScenario, error) {
	return s.local.GetActive(ctx)
}
func (s *Scenario) GetScenario(ctx context.Context, name string) ([]byte, error) {
	return s.local.GetScenario(ctx, name)
}
func (s *Scenario) ListScenarios(ctx context.Context) ([]string, error) {
	return s.local.ListScenarios(ctx)
}
