// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// Handler implements slog.Handler to write logs to the Binary WAL.
type Handler struct {
	writer      *WAL
	gen         *idgen.IDGenerator
	opts        slog.HandlerOptions
	preAttrs    []slog.Attr
	groupPrefix string
	entityID    uint64
	entityName  string
	component   string
}

// NewHandler creates a new WAL Handler.
func NewHandler(w *WAL, gen *idgen.IDGenerator, entityID uint64, entityName string, component string, opts *slog.HandlerOptions) *Handler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &Handler{
		writer:     w,
		gen:        gen,
		opts:       *opts,
		entityID:   entityID,
		entityName: entityName,
		component:  component,
	}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Construct attributes map
	attrs := make(map[string]any)

	// Add pre-formatted attributes
	for _, a := range h.preAttrs {
		attrs[string(a.Key)] = a.Value.Any()
	}

	// Add record level attributes
	r.Attrs(func(a slog.Attr) bool {
		attrs[string(a.Key)] = a.Value.Any()
		return true
	})

	// Inject Component Identity (flux.type) if not present in attributes
	// We use "flux.type" to match new Sink logic
	if _, ok := attrs["flux.type"]; !ok && h.component != "" {
		attrs["flux.type"] = h.component
	}

	// Dynamic Identity Overrides (New: respect component/name from attributes)
	eType := h.component
	if t, ok := attrs["flux.type"].(string); ok && t != "" {
		eType = t
	}
	eName := h.entityName
	if n, ok := attrs["flux.name"].(string); ok && n != "" {
		eName = n
	}

	// Construct FluxMsg Payload
	payload := map[string]any{
		"type":        "log",
		"timestamp":   r.Time.UnixMicro(),
		"severity":    r.Level.String(),
		"body":        r.Message,
		"entity_id":   h.entityID,
		"entity_name": eName,
		"entity_type": eType,
		"attributes":  attrs,
	}

	// Create FluxMsg Envelope
	msg := fluxmsg.New()
	if h.gen != nil {
		id, _ := h.gen.NextFluxID()
		msg.FluxID = id
	}
	msg.Data = payload // payload is "record" or flattened?
	// LogExporter put it in a batch array.
	// Write INDIVIDUAL records to WAL (Binary Sync)
	// FluxMsg type "telemetry.log" (CBOR) is handled by sink

	msg.Metadata["type"] = "telemetry.log" // New type for Individual CBOR Log
	msg.Metadata["scope"] = "telemetry"

	// Write to WAL
	// Note: Writer handles concurrency lock, but Handler.Handle might be called concurrently.
	// Writer is thread-safe.
	return h.writer.Write(msg)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Clone and append
	h2 := *h
	h2.preAttrs = append(h.preAttrs[:len(h.preAttrs):len(h.preAttrs)], attrs...)
	return &h2
}

func (h *Handler) WithGroup(name string) slog.Handler {
	// Simplified group support (prefix keys?)
	// Proper implementation requires complex state.
	// For now, minimal support.
	h2 := *h
	h2.groupPrefix += name + "."
	return &h2
}
