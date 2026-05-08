// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func TestHandler_Handle(t *testing.T) {
	tmpDir := t.TempDir()
	w, _ := Open(tmpDir, nil)
	defer func() { _ = w.Close() }()

	gen, _ := idgen.New(uuid.New())
	h := NewHandler(w, gen, uuid.New(), "test-service", "worker", nil)
	logger := slog.New(h)

	now := time.Now()
	logger.Info("hello world", "foo", "bar")

	// Verify WAL entry
	data, err := w.Read(1)
	if err != nil {
		t.Fatalf("Failed to read record: %v", err)
	}

	var msg fluxmsg.FluxMsg
	if err2 := cbor.Unmarshal(data, &msg); err2 != nil {
		t.Fatalf("Failed to unmarshal FluxMsg: %v", err2)
	}

	if msg.FluxID == uuid.Nil {
		t.Error("Expected non-zero FluxID")
	}

	payload := normalizeMap(msg.Data)
	if payload == nil {
		t.Fatal("Expected non-nil payload")
	}

	if payload["body"] != "hello world" {
		t.Errorf("Expected 'hello world', got %v", payload["body"])
	}

	attrs, ok := payload["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("Expected attributes map, got %T", payload["attributes"])
	}
	if attrs["foo"] != "bar" {
		t.Errorf("Expected foo=bar, got %v", attrs["foo"])
	}

	// Verify timestamp (within sensible range)
	ts := toInt64(payload["timestamp"])
	if ts < now.UnixMicro()-1000000 || ts > now.UnixMicro()+1000000 {
		t.Errorf("Timestamp out of range: %d", ts)
	}
}

func TestHandler_Levels(t *testing.T) {
	tmpDir := t.TempDir()
	w, _ := Open(tmpDir, nil)
	defer func() { _ = w.Close() }()

	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	h := NewHandler(w, nil, uuid.New(), "test", "test", opts)

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Expected INFO to be disabled")
	}
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Expected WARN to be enabled")
	}
}

func TestHandler_WithAttrs(t *testing.T) {
	tmpDir := t.TempDir()
	w, _ := Open(tmpDir, nil)
	defer func() { _ = w.Close() }()

	h := NewHandler(w, nil, uuid.New(), "test", "test", nil)
	h2 := h.WithAttrs([]slog.Attr{slog.String("request_id", "123")})

	logger := slog.New(h2)
	logger.Info("ping")

	data, _ := w.Read(1)
	var msg fluxmsg.FluxMsg
	_ = cbor.Unmarshal(data, &msg)

	payload := normalizeMap(msg.Data)
	if payload == nil {
		t.Fatal("Expected non-nil payload")
	}
	attrs, ok := payload["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("Expected attributes map, got %T", payload["attributes"])
	}

	if attrs["request_id"] != "123" {
		t.Errorf("Expected request_id=123, got %v", attrs["request_id"])
	}
}

func TestHandler_WithGroup(t *testing.T) {
	tmpDir := t.TempDir()
	w, _ := Open(tmpDir, nil)
	defer func() { _ = w.Close() }()

	h := NewHandler(w, nil, uuid.New(), "test", "test", nil)
	h2 := h.WithGroup("meta")

	if h2.(*Handler).groupPrefix != "meta." {
		t.Errorf("Expected prefix meta., got %s", h2.(*Handler).groupPrefix)
	}
}

// Helpers for robust verification

func normalizeMap(m map[string]any) map[string]any {
	res := make(map[string]any)
	for k, v := range m {
		res[k] = normalizeValue(v)
	}
	return res
}

func normalizeValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return normalizeMap(v)
	case map[interface{}]interface{}:
		res := make(map[string]any)
		for k, val := range v {
			res[fmt.Sprintf("%v", k)] = normalizeValue(val)
		}
		return res
	case []interface{}:
		res := make([]any, len(v))
		for i, val := range v {
			res[i] = normalizeValue(val)
		}
		return res
	default:
		return v
	}
}

func toInt64(v any) int64 {
	switch v := v.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		if v > 9223372036854775807 {
			return 0
		}
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
