// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// GetValue extracts a value from FluxMsg using a dot-notation path.
// Supports: "data.field", "meta.header", "flux_id", "trace_id", "src_id".
func GetValue(msg *fluxmsg.FluxMsg, path string) (any, bool) {
	if path == "payload" {
		return string(msg.RawPayload), true
	}
	if path == "flux_id" {
		return msg.FluxID, true
	}
	if path == "trace_id" {
		return msg.TraceID, true
	}
	if path == "src_id" {
		return msg.SrcGearID, true
	}

	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil, false
	}

	root := parts[0]
	rest := parts[1:]

	if root == "meta" {
		// Meta is flat string map, but keys might contain dots (namespaced)
		// e.g. "meta.iso8583.raw_header" -> "iso8583.raw_header"
		key := strings.Join(rest, ".")
		val, ok := msg.Metadata[key]
		return val, ok
	}

	if root == "data" {
		// Data is map[string]any
		current := msg.Data
		for i, part := range rest {
			val, ok := current[part]
			if !ok {
				return nil, false
			}
			if i == len(rest)-1 {
				return val, true
			}
			// Descent
			next, ok := asStringMap(val)
			if !ok {
				// Path mismatch (not a map)
				return nil, false
			}
			current = next
		}
	}

	return nil, false
}

// asStringMap accepts either shape a nested object can take on a FluxMsg.
//
// A message built in process carries map[string]any, but CBOR decodes an object
// into map[any]any, so the same field is one shape before a bus hop and the
// other after it. Descending only into map[string]any therefore made any nested
// path resolve locally and fail once the message had crossed the bus, with no
// error anywhere: the caller simply saw a missing field.
func asStringMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

// JoinKeys Helper to create composite keys (concat with underscores).
func JoinKeys(parts ...string) string {
	return strings.Join(parts, "_")
}

func NewMockGearContext(config map[string]any) GearContext {
	mid := uuid.New()
	idGen, _ := idgen.New(mid)
	return &mockGearContext{
		ctx:       context.Background(),
		config:    config,
		logger:    slog.Default(),
		idGen:     idGen,
		machineID: mid,
	}
}

type mockGearContext struct {
	ctx       context.Context
	config    map[string]any
	logger    *slog.Logger
	idGen     *idgen.IDGenerator
	machineID uuid.UUID
}

func (m *mockGearContext) Context() context.Context { return m.ctx }
func (m *mockGearContext) Config() map[string]any   { return m.config }
func (m *mockGearContext) GearName() string         { return "mock_gear" }
func (m *mockGearContext) MachineID() uuid.UUID     { return m.machineID }
func (m *mockGearContext) Logger() *slog.Logger     { return m.logger }
func (m *mockGearContext) IDGen() IDGenerator       { return m.idGen }
func (m *mockGearContext) Bus() bus.Bus             { return nil } // Mock bus not needed for basic tests
func (m *mockGearContext) Manager() manager.Manager { return nil }
func (m *mockGearContext) ControlPlane() any        { return nil }
func (m *mockGearContext) ClusterPublicKey() []byte { return nil }
func (m *mockGearContext) Emitter() PortEmitter     { return noopEmitter{} }

// noopEmitter discards emissions; the mock context is for unit tests that
// exercise gear config/logic, not the wired emit path.
type noopEmitter struct{}

func (noopEmitter) Emit(string, *fluxmsg.FluxMsg) error { return nil }

// NewNoopEmitter returns a PortEmitter that discards everything. Gear unit
// tests use it so a gear that calls ctx.Emitter().Emit(...) does not nil-panic.
func NewNoopEmitter() PortEmitter { return noopEmitter{} }
