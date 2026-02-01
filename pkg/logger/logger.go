// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"sync/atomic"
)

// EntityType defines the source of the log (aligns with Entity Type Table in protocols.md)
type EntityType string

const (
	TypeMixer    EntityType = "MIXER"
	TypeRack     EntityType = "RACK"
	TypeGear     EntityType = "GEAR"
	TypeSnake    EntityType = "SNAKE"
	TypeScenario EntityType = "SCENARIO"
)

// LevelTrace is a custom log level for high-volume dumps
const LevelTrace = slog.Level(-8)

// AtomicLevel manages log level safely across goroutines
type AtomicLevel struct {
	val atomic.Int64
}

func NewAtomicLevel(l slog.Level) *AtomicLevel {
	al := &AtomicLevel{}
	al.Set(l)
	return al
}

func (al *AtomicLevel) Set(l slog.Level) {
	al.val.Store(int64(l))
}

func (al *AtomicLevel) Level() slog.Level {
	return slog.Level(al.val.Load())
}

// Config holds logger configuration
type Config struct {
	Level      string
	EntityType EntityType
	Name       string
	Writer     io.Writer // Defaults to os.Stdout if nil
}

// FluxHandler defines the handler structure (exported so we can access SetLevel)
type FluxHandler struct {
	w         io.Writer
	level     *AtomicLevel
	component string
	name      string
	attrs     []slog.Attr
	group     string
}

// New creates a standardized logger with custom formatting
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}

	handler := &FluxHandler{
		w:         w,
		level:     NewAtomicLevel(ParseLevel(cfg.Level)),
		component: string(cfg.EntityType),
		name:      cfg.Name,
	}

	return slog.New(handler)
}

// wrapper interface for handlers that wrap another handler (e.g. BufferHandler)
type wrapper interface {
	Next() slog.Handler
}

// collection interface for handlers that manage multiple handlers (e.g. MultiHandler)
type collection interface {
	Handlers() []slog.Handler
}

// SetLevel updates the log level dynamically
func SetLevel(logger *slog.Logger, level string) {
	// Queue of handlers to visit (BFS)
	queue := []slog.Handler{logger.Handler()}
	newLevel := ParseLevel(level)

	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]

		// Check for FluxHandler (Target)
		if fh, ok := h.(*FluxHandler); ok {
			fh.level.Set(newLevel)
			continue
		}

		// Check for Wrapper (e.g., BufferHandler, SourceHandler)
		if w, ok := h.(wrapper); ok {
			queue = append(queue, w.Next())
		}

		// Check for Collection (e.g., MultiHandler)
		if c, ok := h.(collection); ok {
			queue = append(queue, c.Handlers()...)
		}
	}
}

// WithComponent creates a child logger with a specific component and name.
// Always uses parent.With() to ensure OTel handlers are preserved.
func WithComponent(parent *slog.Logger, entityType EntityType, name string) *slog.Logger {
	return parent.With(
		"component", string(entityType),
		"name", name,
		"flux.type", string(entityType),
		"flux.name", name,
	)
}

// ParseLevel converts string to slog.Level (case-insensitive)
func ParseLevel(l string) slog.Level {
	switch strings.ToLower(l) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (h *FluxHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *FluxHandler) Handle(_ context.Context, r slog.Record) error {
	// Fixed header parts
	// 1. Time (RFC3339 Milli)
	// Example: 2025-12-20T16:28:24.123-03:00
	const layout = "2006-01-02T15:04:05.000Z07:00"
	ts := r.Time.Format(layout)

	// 2. Level (Fixed width alignment preferred)
	lvl := r.Level.String()
	if r.Level == LevelTrace {
		lvl = "TRACE"
	}

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
				// Strip "fluxrig/" to get path relative to repo root
				file = file[idx+len("fluxrig/"):]
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

	// 5. Attributes (Grouped under one pipe)
	// Filter redundant code.* attributes
	firstAttr := true

	// Helper to process attrs
	processAttr := func(a slog.Attr) {
		if a.Key == "code.file.path" || a.Key == "code.line.number" || a.Key == "code.function.name" {
			return
		}
		if firstAttr {
			buf = append(buf, " | "...)
			firstAttr = false
		} else {
			buf = append(buf, " "...)
		}
		buf = h.appendAttr(buf, a)
	}

	for _, a := range h.attrs {
		processAttr(a)
	}

	r.Attrs(func(a slog.Attr) bool {
		processAttr(a)
		return true
	})

	buf = append(buf, '\n')

	_, err := h.w.Write(buf)
	return err
}

func (h *FluxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Extract component and name from attrs and update internal fields
	// This ensures the log header shows the correct identity
	newComponent := h.component
	newName := h.name
	filteredAttrs := make([]slog.Attr, 0, len(attrs))

	for _, a := range attrs {
		switch a.Key {
		case "component":
			// Use the most specific component (last one wins)
			newComponent = a.Value.String()
		case "name":
			newName = a.Value.String()
		default:
			filteredAttrs = append(filteredAttrs, a)
		}
	}

	// Combine parent attrs with filtered new attrs (excluding component/name)
	allAttrs := make([]slog.Attr, len(h.attrs)+len(filteredAttrs))
	copy(allAttrs, h.attrs)
	copy(allAttrs[len(h.attrs):], filteredAttrs)

	return &FluxHandler{
		w:         h.w,
		level:     h.level,
		component: newComponent,
		name:      newName,
		attrs:     allAttrs,
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
