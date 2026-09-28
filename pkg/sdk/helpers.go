// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// GetValue extracts a value from FluxMsg using a dot-notation path.
// Supports: "data.field", "meta.header", "flux_id", "trace_id", "src_id", and
// (see the fallback below) a bare path with neither prefix.
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

	if rest, ok := strings.CutPrefix(path, "meta."); ok {
		// Meta is flat string map, but keys might contain dots (namespaced)
		// e.g. "meta.iso8583.raw_header" -> "iso8583.raw_header"
		val, ok := msg.Metadata[rest]
		return val, ok
	}

	if rest, ok := strings.CutPrefix(path, "data."); ok {
		return msg.Get(rest)
	}

	// A path with neither prefix used to return (nil, false) unconditionally,
	// a second, stricter key-resolution dialect living alongside FluxMsg.Get's
	// own permissive one (no prefix required at all), which the rest of the
	// codebase already uses for a path like "iso8583.field.2" (the conductor,
	// the iso8583 codec, and elsewhere). A caller — coatcheck's key_fields and
	// value_fields, configured the way every other gear's field paths are —
	// found nothing here even though the identical string already resolved
	// through msg.Get. Falling back to it accepts both dialects instead of
	// silently failing the second one.
	return msg.Get(path)
}

// FieldToKeyPart renders a structured field value into a string for a
// correlation or routing key, canonically: coatcheck's extractKey and the
// conductor's route matching and correlation-key building all derive keys
// from field values that can be []byte (a PAN, a Track2 blob, PIN/MAC/EMV
// binary), and each picking its own rendering silently derived a different
// key from the identical underlying value on each side.
func FieldToKeyPart(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		return fmt.Sprint(val)
	}
}

// JoinKeys joins key parts unambiguously via netstring-style length
// prefixing: len(part), ":", part, with no separator of its own, repeated for
// each part. No two distinct sequences of parts can ever produce the same
// joined string this way, unlike the fixed separator this replaced ("_", or
// the conductor's own "\x1f" unit separator): a structured field can be
// arbitrary binary (a PIN block, a MAC, an EMV tag) with no byte excluded, so
// "a_b"+"c" and "a"+"b_c" (or the \x1f equivalent) joined the same way were
// indistinguishable whenever a part's own bytes happened to contain the
// separator.
func JoinKeys(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
	}
	return b.String()
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
