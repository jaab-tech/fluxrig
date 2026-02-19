// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"log/slog"

	"github.com/ThreeDotsLabs/watermill"
)

// WatermillAdapter adapts slog to Watermill's LoggerAdapter interface.
// Uses slog.Default() dynamically to ensure logs always go through the current
// OTel provider, even after UpdateIdentity reinitializes telemetry.
// Watermill is internal to Mixer, so logs inherit the MIXER entity type.
type WatermillAdapter struct {
	fields watermill.LogFields
}

// NewWatermillAdapter creates a new Watermill logger adapter.
// The logger parameter is ignored - we always use slog.Default() at log time.
func NewWatermillAdapter(l *slog.Logger) *WatermillAdapter {
	return &WatermillAdapter{
		fields: make(watermill.LogFields),
	}
}

// logger returns the current OTel-connected logger.
// Does NOT add component attr - Watermill inherits the parent entity type (MIXER).
// The source file (watermill.go:XX) identifies this as Watermill code.
func (w *WatermillAdapter) logger() *slog.Logger {
	return slog.Default()
}

func (w *WatermillAdapter) Error(msg string, err error, fields watermill.LogFields) {
	attrs := w.toAttrs(fields)
	attrs = append(attrs, slog.Any("error", err))
	w.logger().Error(msg, attrs...)
}

func (w *WatermillAdapter) Info(msg string, fields watermill.LogFields) {
	w.logger().Info(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) Debug(msg string, fields watermill.LogFields) {
	w.logger().Debug(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) Trace(msg string, fields watermill.LogFields) {
	// Map Trace to Debug
	w.logger().Debug(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	newAttrs := w.fields.Add(fields)
	return &WatermillAdapter{
		fields: newAttrs,
	}
}

func (w *WatermillAdapter) toAttrs(fields watermill.LogFields) []any {
	if len(fields) == 0 {
		return nil
	}
	attrs := make([]any, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	return attrs
}
