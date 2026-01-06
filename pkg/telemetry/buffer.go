package telemetry

import (
	"context"
	"log/slog"
	"sync"
)

// Buffer captures logs for later replay.
type Buffer struct {
	mu      sync.Mutex
	records []slog.Record
}

// BufferHandler wraps a handler and captures all records to a shared Buffer.
type BufferHandler struct {
	buffer *Buffer
	next   slog.Handler
}

// NewBufferHandler creates a handler that writes to next and buffers records.
func NewBufferHandler(next slog.Handler) *BufferHandler {
	return &BufferHandler{
		buffer: &Buffer{},
		next:   next,
	}
}

func (h *BufferHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *BufferHandler) Handle(ctx context.Context, r slog.Record) error {
	h.buffer.mu.Lock()
	// Store a clone to ensure attributes are preserved at this point in time
	h.buffer.records = append(h.buffer.records, r.Clone())
	h.buffer.mu.Unlock()
	return h.next.Handle(ctx, r)
}

func (h *BufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BufferHandler{
		buffer: h.buffer,
		next:   h.next.WithAttrs(attrs),
	}
}

func (h *BufferHandler) WithGroup(name string) slog.Handler {
	return &BufferHandler{
		buffer: h.buffer,
		next:   h.next.WithGroup(name),
	}
}

// FlushTo replays buffered records to the target handler.
func (h *BufferHandler) FlushTo(ctx context.Context, target slog.Handler) error {
	h.buffer.mu.Lock()
	// Capture and clear buffer
	pending := h.buffer.records
	h.buffer.records = nil
	h.buffer.mu.Unlock()

	for _, r := range pending {
		if target.Enabled(ctx, r.Level) {
			if err := target.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}
