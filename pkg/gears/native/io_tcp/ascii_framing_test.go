// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scanAll(t *testing.T, cfg *Config, wire string) ([]string, error) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(wire))
	sc.Buffer(make([]byte, 0, 4096), maxScanTokenSize)
	sc.Split(MakeSplitter(cfg))
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

func TestLengthASCII4_FrameOutbound(t *testing.T) {
	f, err := frameOutbound(FramingLengthASCII4, []byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, "0005hello", string(f))

	_, err = frameOutbound(FramingLengthASCII4, bytes.Repeat([]byte("x"), 10000))
	assert.Error(t, err)
}

func TestLengthASCII4_RoundTrip(t *testing.T) {
	cfg := &Config{Framing: FramingLengthASCII4}
	var wire bytes.Buffer
	msgs := []string{"one", "second message", strings.Repeat("z", 9999)}
	for _, m := range msgs {
		f, err := frameOutbound(cfg.Framing, []byte(m))
		require.NoError(t, err)
		wire.Write(f)
	}
	got, err := scanAll(t, cfg, wire.String())
	require.NoError(t, err)
	assert.Equal(t, msgs, got)
}

func TestLengthASCII4_Malformed(t *testing.T) {
	cfg := &Config{Framing: FramingLengthASCII4}
	for name, wire := range map[string]string{
		"letters":    "01abxxxxxxxxxx",
		"binary":     "\x00\x05\x00\x00hello",
		"sign":       "+005hello",
		"short body": "0010abc",
		"short head": "01",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanAll(t, cfg, wire)
			assert.Error(t, err)
		})
	}
}
