// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
)

func TestEncodeFrame_ASCII(t *testing.T) {
	p := bytes.Repeat([]byte("a"), 123)
	f := encodeFrame(p, 4, true, "big")
	if string(f[:4]) != "0123" || !bytes.Equal(f[4:], p) {
		t.Fatalf("bad frame: %q", f[:4])
	}
	n, err := parseLengthPrefix(f[:4], true, "big")
	if err != nil || n != 123 {
		t.Fatalf("parse = %d, %v", n, err)
	}
}

func TestEncodeFrame_BinaryRoundTrip(t *testing.T) {
	p := []byte("abc")
	for _, size := range []int{2, 4} {
		for _, e := range []string{"big", "little"} {
			f := encodeFrame(p, size, false, e)
			n, err := parseLengthPrefix(f[:size], false, e)
			if err != nil || n != 3 {
				t.Fatalf("size=%d endian=%s: %d %v", size, e, n, err)
			}
		}
	}
}

func TestParseLengthPrefix_ASCIIMalformed(t *testing.T) {
	for _, h := range []string{"01ab", "\x00\x05\x00\x00", "+123", " 123"} {
		if _, err := parseLengthPrefix([]byte(h), true, "big"); err == nil {
			t.Errorf("expected error for %q", h)
		}
	}
}
