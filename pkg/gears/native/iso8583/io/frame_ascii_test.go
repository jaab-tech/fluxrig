// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func asciiCfg(size int, includes bool) *Config {
	cfg := DefaultConfig()
	cfg.FrameLengthEncoding = FrameEncodingASCIIDecimal
	cfg.FrameLengthSize = size
	cfg.FrameIncludesHeader = includes
	cfg.ReadTimeout = Duration(2 * time.Second)
	return &cfg
}

// frameCodec abstracts Server and Client, which share the same framing.
type frameCodec struct {
	name  string
	build func(cfg *Config, p []byte) ([]byte, error)
	read  func(cfg *Config, c net.Conn) ([]byte, error)
}

func codecs() []frameCodec {
	return []frameCodec{
		{"server",
			func(cfg *Config, p []byte) ([]byte, error) {
				return (&Server{config: cfg, log: slog.New(slog.DiscardHandler)}).buildFrame(p)
			},
			func(cfg *Config, c net.Conn) ([]byte, error) {
				return (&Server{config: cfg, log: slog.New(slog.DiscardHandler)}).readFrame(c)
			}},
		{"client",
			func(cfg *Config, p []byte) ([]byte, error) {
				return (&Client{config: cfg, log: slog.New(slog.DiscardHandler)}).buildFrame(p)
			},
			func(cfg *Config, c net.Conn) ([]byte, error) {
				return (&Client{config: cfg, log: slog.New(slog.DiscardHandler)}).readFrame(c)
			}},
	}
}

func TestASCIIFrame_Build(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 123)
	for _, k := range codecs() {
		t.Run(k.name, func(t *testing.T) {
			f, err := k.build(asciiCfg(4, false), payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(f[:4]) != "0123" || !bytes.Equal(f[4:], payload) {
				t.Fatalf("bad frame prefix %q", f[:4])
			}
			f, err = k.build(asciiCfg(4, true), payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(f[:4]) != "0127" {
				t.Fatalf("frame_includes_header prefix = %q, want 0127", f[:4])
			}
			f, _ = k.build(asciiCfg(6, false), payload)
			if string(f[:6]) != "000123" {
				t.Fatalf("6 digit prefix = %q", f[:6])
			}
		})
	}
}

func TestASCIIFrame_BuildTooLarge(t *testing.T) {
	for _, k := range codecs() {
		t.Run(k.name, func(t *testing.T) {
			if _, err := k.build(asciiCfg(2, false), make([]byte, 100)); err == nil {
				t.Fatal("expected error: 100 does not fit 2 digits")
			}
			if _, err := k.build(asciiCfg(4, true), make([]byte, 9997)); err == nil {
				t.Fatal("expected error: 9997+4 does not fit 4 digits")
			}
		})
	}
}

func TestASCIIFrame_RoundTrip(t *testing.T) {
	for _, k := range codecs() {
		for _, includes := range []bool{false, true} {
			t.Run(k.name, func(t *testing.T) {
				cfg := asciiCfg(4, includes)
				payload := []byte("0800822000000000000004000000000000001234567890")
				f, err := k.build(cfg, payload)
				if err != nil {
					t.Fatal(err)
				}
				a, b := net.Pipe()
				defer func() { _ = a.Close() }()
				defer func() { _ = b.Close() }()
				go func() { _, _ = a.Write(f) }()
				got, err := k.read(cfg, b)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("got %q want %q", got, payload)
				}
			})
		}
	}
}

func TestASCIIFrame_ReadMalformed(t *testing.T) {
	cases := map[string]string{
		"letters":        "01ab" + strings.Repeat("x", 10),
		"binary prefix":  "\x00\x7b\x00\x00" + strings.Repeat("x", 10),
		"sign":           "+123" + strings.Repeat("x", 10),
		"space":          " 123" + strings.Repeat("x", 10),
		"zero length":    "0000",
		"too small incl": "0003", // with frame_includes_header, length 3-4 <= 0
	}
	for _, k := range codecs() {
		for name, wire := range cases {
			t.Run(k.name+"/"+name, func(t *testing.T) {
				cfg := asciiCfg(4, name == "too small incl")
				a, b := net.Pipe()
				defer func() { _ = a.Close() }()
				defer func() { _ = b.Close() }()
				go func() { _, _ = a.Write([]byte(wire)) }()
				if _, err := k.read(cfg, b); err == nil {
					t.Fatal("expected error")
				}
			})
		}
	}
}

func TestASCIIFrame_ReadTooLong(t *testing.T) {
	for _, k := range codecs() {
		t.Run(k.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer func() { _ = a.Close() }()
			defer func() { _ = b.Close() }()
			go func() { _, _ = a.Write([]byte("99999")) }()
			if _, err := k.read(asciiCfg(5, false), b); err == nil {
				t.Fatal("expected error for length above max")
			}
		})
	}
}

func TestASCIIFrame_ReadTruncatedBody(t *testing.T) {
	for _, k := range codecs() {
		t.Run(k.name, func(t *testing.T) {
			cfg := asciiCfg(4, false)
			a, b := net.Pipe()
			defer func() { _ = b.Close() }()
			go func() { _, _ = a.Write([]byte("0010abc")); _ = a.Close() }()
			if _, err := k.read(cfg, b); err == nil {
				t.Fatal("expected error for truncated body")
			}
		})
	}
}

func TestASCIIFrame_BinaryUnchanged(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyDefaults()
	f, err := (&Server{config: &cfg, log: slog.New(slog.DiscardHandler)}).buildFrame([]byte("abc"))
	if err != nil || !bytes.Equal(f, []byte{0, 3, 'a', 'b', 'c'}) {
		t.Fatalf("binary default changed: %v %v", f, err)
	}
}

func TestConfig_FrameLengthEncoding(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{"mode": "server", "bind": ":0", "frame_length_encoding": "ascii_decimal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.FrameLengthSize != 4 {
		t.Fatalf("ascii_decimal default size = %d, want 4", cfg.FrameLengthSize)
	}

	cfg, _ = ParseConfig(map[string]any{"mode": "server", "bind": ":0", "frame_length_encoding": "ascii_decimal", "frame_length_size": 6})
	if err := cfg.Validate(); err != nil || cfg.FrameLengthSize != 6 {
		t.Fatalf("explicit size: %v %d", err, cfg.FrameLengthSize)
	}

	bad := []map[string]any{
		{"frame_length_encoding": "hex"},
		{"frame_length_encoding": "ascii_decimal", "frame_length_size": -1},
		{"frame_length_encoding": "ascii_decimal", "frame_length_size": 10},
		{"frame_length_encoding": "binary", "frame_length_size": 3},
	}
	for _, m := range bad {
		m["mode"], m["bind"] = "server", ":0"
		c, err := ParseConfig(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil {
			t.Errorf("expected validation error for %v", m)
		}
	}

	// Default stays binary, size 2.
	cfg, _ = ParseConfig(map[string]any{"mode": "server", "bind": ":0"})
	if err := cfg.Validate(); err != nil || cfg.FrameLengthEncoding != FrameEncodingBinary || cfg.FrameLengthSize != 2 {
		t.Fatalf("default: %v %+v", err, cfg)
	}
}
