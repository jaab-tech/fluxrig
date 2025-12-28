package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := Config{
		Level:     "debug",
		Component: TypeMixer,
		Name:      "test-mixer",
		Writer:    buf,
	}

	l := New(cfg)
	l.Debug("Hello Debug", "key", "val")

	out := buf.String()
	// Check header parts
	if !strings.Contains(out, "DEBUG") {
		t.Error("Missing Level")
	}
	if !strings.Contains(out, "MIXER") {
		t.Error("Missing Component")
	}
	if !strings.Contains(out, "test-mixer") {
		t.Error("Missing Name")
	}
	if !strings.Contains(out, "Hello Debug") {
		t.Error("Missing Message")
	}
	if !strings.Contains(out, `key="val"`) {
		t.Errorf("Missing Attribute: %s", out)
	}
}

func TestLogLevels(t *testing.T) {
	tests := []struct {
		cfgLevel string
		msgLevel slog.Level
		expect   bool
	}{
		{"info", slog.LevelDebug, false},
		{"info", slog.LevelInfo, true},
		{"warn", slog.LevelInfo, false},
		{"error", slog.LevelWarn, false},
		{"debug", slog.LevelDebug, true},
	}

	for _, tt := range tests {
		buf := &bytes.Buffer{}
		cfg := Config{
			Level:  tt.cfgLevel,
			Writer: buf,
		}
		l := New(cfg)
		l.Log(context.Background(), tt.msgLevel, "msg")

		hasOutput := len(buf.Bytes()) > 0
		if hasOutput != tt.expect {
			t.Errorf("Level %s, Msg %s: want %v got %v", tt.cfgLevel, tt.msgLevel, tt.expect, hasOutput)
		}
	}
}

func TestFormatting(t *testing.T) {
	buf := &bytes.Buffer{}
	h := &FluxHandler{
		w:         buf,
		level:     slog.LevelInfo,
		component: "COMP",
		name:      "NAME",
	}
	l := slog.New(h)

	// Test indent
	l.Info("Line1\nLine2")

	out := buf.String()
	// Line2 should be indented
	// We don't check exact timestamp length but we know the structure.
	if !strings.Contains(out, "Line1") {
		t.Error("Missing output")
	}
	// Indentation check is tricky without regex, but check for newlines
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("Expected indentation splitting message, got %d lines", len(lines))
	}
}

func TestWithAttrs(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := Config{Level: "info", Writer: buf}
	l := New(cfg).With("common", "val")

	l.Info("msg", "req", 123)

	out := buf.String()
	if !strings.Contains(out, `common="val"`) {
		t.Error("Missing common attr")
	}
	if !strings.Contains(out, "req=123") {
		t.Error("Missing req attr")
	}

	// Test Uint and Bool (Default)
	buf.Reset()
	l.Info("msg", "u", uint64(999), "b", true)
	out = buf.String()
	if !strings.Contains(out, "u=999") {
		t.Error("Missing uint64 attr")
	}
	if !strings.Contains(out, "b=true") {
		t.Error("Missing bool attr")
	}
}

func TestWithGroup(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := Config{Level: "info", Writer: buf}
	l := New(cfg).WithGroup("grp")

	l.Info("msg", "k", "v")

	out := buf.String()
	if !strings.Contains(out, `grp.k="v"`) {
		t.Errorf("Missing group prefix: %s", out)
	}
}
