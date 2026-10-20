// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// An ISO 8583 body is opaque to the io gear; this one starts like an ASCII 0800.
var tpduTestBody = []byte("0800822000000000000004000000000000000102030405000042301")

type tpduSink struct {
	mu   sync.Mutex
	msgs []*fluxmsg.FluxMsg
}

func (s *tpduSink) emit(m *fluxmsg.FluxMsg) {
	s.mu.Lock()
	s.msgs = append(s.msgs, m)
	s.mu.Unlock()
}

func (s *tpduSink) first(t *testing.T) *fluxmsg.FluxMsg {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.msgs) > 0 {
			m := s.msgs[0]
			s.mu.Unlock()
			return m
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no message emitted")
	return nil
}

func tpduHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// frameWith builds a 2 byte big endian length frame carrying header and body.
func frameWith(header []byte) []byte {
	n := len(header) + len(tpduTestBody)
	out := []byte{byte(n >> 8), byte(n)}
	out = append(out, header...)
	return append(out, tpduTestBody...)
}

// A server that preserved a TPDU must send it back with the destination and
// source addresses exchanged (60 DDDD SSSS -> 60 SSSS DDDD), unless tpdu_swap is
// off. Before, tpdu_swap was documented with a default of true but never read.
func TestServerReplySwapsTPDUAddresses(t *testing.T) {
	for _, swap := range []bool{true, false} {
		name := "swap on"
		if !swap {
			name = "swap off"
		}
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Mode = ModeServer
			cfg.Bind = "127.0.0.1:0"
			cfg.FrameLengthSize = 2
			cfg.TPDUEnabled = true
			cfg.TPDUSwap = swap
			cfg.PreserveHeaders = true
			cfg.ReadTimeout = Duration(2 * time.Second)
			cfg.WriteTimeout = Duration(2 * time.Second)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			gen, err := idgen.New(uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			sink := &tpduSink{}
			srv := NewServer(&cfg, slog.New(slog.DiscardHandler), sink.emit, gen)
			if err = srv.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Stop() })

			c, err := net.Dial("tcp", srv.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })

			tpdu := tpduHex("6000030001") // destination 0003, source 0001
			if _, err := c.Write(frameWith(tpdu)); err != nil {
				t.Fatal(err)
			}
			m := sink.first(t)
			if !bytes.Equal(m.RawPayload, tpduTestBody) {
				t.Fatalf("payload = %q, want the message without the TPDU", m.RawPayload)
			}
			if got := m.Metadata["iso8583.raw_header"]; got != "hex:6000030001" {
				t.Fatalf("raw_header = %q", got)
			}
			if _, err := srv.Process(context.Background(), m); err != nil {
				t.Fatal(err)
			}

			want := frameWith(tpdu)
			if swap {
				want = frameWith(tpduHex("6000010003"))
			}
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			got := make([]byte, len(want))
			if _, err := io.ReadFull(c, got); err != nil {
				t.Fatalf("reading the reply: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("reply on the wire = %x, want %x", got, want)
			}
		})
	}
}

func TestSwapTPDU(t *testing.T) {
	if got := swapTPDU(tpduHex("60AABBCCDD"), 5); !bytes.Equal(got, tpduHex("60CCDDAABB")) {
		t.Errorf("swap = %x", got)
	}
	in := tpduHex("60AABBCCDD")
	_ = swapTPDU(in, 5)
	if !bytes.Equal(in, tpduHex("60AABBCCDD")) {
		t.Error("swapTPDU modified its input")
	}
	if got := swapTPDU(tpduHex("60AABB"), 5); !bytes.Equal(got, tpduHex("60AABB")) {
		t.Errorf("short header changed: %x", got)
	}
	if got := swapTPDU(tpduHex("60AABBCCDD"), 7); !bytes.Equal(got, tpduHex("60AABBCCDD")) {
		t.Errorf("non-5 byte tpdu_length must leave the header alone: %x", got)
	}
}

// Client mode reads the same frames. A preserved header is binary (bytes >= 0x80
// here), so it must be kept hex-encoded or the message fails Validate.
func TestClientPreservesBinaryHeaderHexEncoded(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	cfg := DefaultConfig()
	cfg.Mode = ModeClient
	cfg.Connect = l.Addr().String()
	cfg.FrameLengthSize = 2
	cfg.TPDUEnabled = true
	cfg.PreserveHeaders = true
	cfg.ReconnectWait = Duration(100 * time.Millisecond)
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := idgen.New(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	sink := &tpduSink{}
	cl := NewClient(&cfg, slog.New(slog.DiscardHandler), sink.emit, gen)
	if err = cl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Stop() })

	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(frameWith(tpduHex("60F0F1F2F3"))); err != nil {
		t.Fatal(err)
	}
	m := sink.first(t)
	if !bytes.Equal(m.RawPayload, tpduTestBody) {
		t.Fatalf("payload = %q", m.RawPayload)
	}
	if got := m.Metadata["iso8583.raw_header"]; got != "hex:60f0f1f2f3" {
		t.Fatalf("raw_header = %q, want hex:60f0f1f2f3", got)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("message with a preserved binary header is not valid: %v", err)
	}
}
