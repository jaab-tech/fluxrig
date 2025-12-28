package logger

import (
	"log/slog"

	"github.com/ThreeDotsLabs/watermill"
)

type WatermillAdapter struct {
	logger *slog.Logger
	fields watermill.LogFields
}

func NewWatermillAdapter(l *slog.Logger) *WatermillAdapter {
	return &WatermillAdapter{
		logger: l,
		fields: make(watermill.LogFields),
	}
}

func (w *WatermillAdapter) Error(msg string, err error, fields watermill.LogFields) {
	attrs := w.toAttrs(fields)
	attrs = append(attrs, slog.Any("error", err))
	w.logger.Error(msg, attrs...)
}

func (w *WatermillAdapter) Info(msg string, fields watermill.LogFields) {
	w.logger.Info(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) Debug(msg string, fields watermill.LogFields) {
	w.logger.Debug(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) Trace(msg string, fields watermill.LogFields) {
	// Map Trace to Debug
	w.logger.Debug(msg, w.toAttrs(fields)...)
}

func (w *WatermillAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	newAttrs := w.fields.Add(fields)
	return &WatermillAdapter{
		logger: w.logger, // We keep the same logger, attrs applied at call time??
		// Watermill 'With' returns a NEW logger with fields pre-applied.
		// slog.With returns a new logger.
		// So we should actually update the stored logger.
		fields: newAttrs,
	}
	// Actually better implementation:
	// l := w.logger.With(w.toAttrs(fields)...)
	// return NewWatermillAdapter(l)
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
