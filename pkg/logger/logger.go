package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ComponentType defines the source of the log
type ComponentType string

const (
	TypeMixer ComponentType = "MIXER"
	TypeRack  ComponentType = "RACK"
	TypeGear  ComponentType = "GEAR"
)

// Config holds logger configuration
type Config struct {
	Level     string
	Component ComponentType
	Name      string
	Writer    io.Writer // Defaults to os.Stdout if nil
}

// New creates a standardized logger with custom formatting
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}

	// Use custom handler for strict formatting
	handler := &FluxHandler{
		w:         w,
		level:     parseLevel(cfg.Level),
		component: string(cfg.Component),
		name:      cfg.Name,
	}

	return slog.New(handler)
}

func parseLevel(l string) slog.Level {
	switch l {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// FluxHandler implements slog.Handler with custom formatting:
// TIMESTAMP | LEVEL | COMPONENT | NAME | MESSAGE | ATTRS...
type FluxHandler struct {
	w         io.Writer
	level     slog.Level
	component string
	name      string
	attrs     []slog.Attr
	group     string
}

func (h *FluxHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *FluxHandler) Handle(_ context.Context, r slog.Record) error {
	// Fixed header parts
	// 1. Time (RFC3339 Milli)
	// Example: 2025-12-20T16:28:24.123-03:00
	const layout = "2006-01-02T15:04:05.000Z07:00"
	ts := r.Time.Format(layout)

	// 2. Level (Fixed width alignment preferred)
	lvl := r.Level.String()

	// 3. Component & Name
	// 4. Message
	msg := r.Message

	// Buffer construction
	buf := make([]byte, 0, 1024)

	buf = append(buf, ts...)
	buf = append(buf, " | "...)

	buf = append(buf, lvl...)
	buf = append(buf, " | "...)

	if h.component != "" {
		buf = append(buf, h.component...)
		buf = append(buf, " | "...)
	}

	if h.name != "" {
		buf = append(buf, h.name...)
		buf = append(buf, " | "...)
	}

	// NEW: Source Location
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		if f.File != "" {
			// Trim to project root if possible, or just base
			// Simple heuristics: find "fluxrig/" or base
			file := f.File
			if idx := strings.Index(file, "fluxrig/"); idx != -1 {
				file = file[idx:]
			} else {
				file = filepath.Base(file)
			}
			buf = append(buf, file...)
			buf = append(buf, ':')
			buf = strconv.AppendInt(buf, int64(f.Line), 10)
			buf = append(buf, " | "...)
		}
	}

	// Calculate prefix length for multi-line indentation
	// Align subsequent lines with the start of the message text.
	prefixLen := len(buf)
	indent := "\n" + strings.Repeat(" ", prefixLen)

	// Replace newlines in message with indented newlines for readability.
	msg = strings.ReplaceAll(msg, "\n", indent)
	buf = append(buf, msg...)

	// 5. Attributes (Pipe separated? "define a | separated fields")
	// Pre-computed fields
	for _, a := range h.attrs {
		buf = append(buf, " | "...)
		buf = h.appendAttr(buf, a)
	}

	// Record attributes
	r.Attrs(func(a slog.Attr) bool {
		buf = append(buf, " | "...)
		buf = h.appendAttr(buf, a)
		return true
	})

	buf = append(buf, '\n')

	_, err := h.w.Write(buf)
	return err
}

func (h *FluxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &FluxHandler{
		w:         h.w,
		level:     h.level,
		component: h.component,
		name:      h.name,
		attrs:     newAttrs,
		group:     h.group,
	}
}

func (h *FluxHandler) WithGroup(name string) slog.Handler {
	return &FluxHandler{
		w:         h.w,
		level:     h.level,
		component: h.component,
		name:      h.name,
		attrs:     h.attrs,
		group:     h.group + name + ".",
	}
}

func (h *FluxHandler) appendAttr(b []byte, a slog.Attr) []byte {
	// key=value
	if h.group != "" {
		b = append(b, h.group...)
	}
	b = append(b, a.Key...)
	b = append(b, '=')

	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindString:
		b = strconv.AppendQuote(b, val.String())
	case slog.KindInt64:
		b = strconv.AppendInt(b, val.Int64(), 10)
	case slog.KindUint64:
		b = strconv.AppendUint(b, val.Uint64(), 10)
	default:
		b = append(b, val.String()...)
	}
	return b
}
