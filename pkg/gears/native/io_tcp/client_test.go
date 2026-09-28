// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"context"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Before this fix, a client with ReadResponses: false never read its socket at
// all, so a peer that closed or reset the connection was never noticed:
// handleConn blocked on <-c.done forever, and runLoop's dial/backoff
// reconnect logic never ran again until the gear was stopped.
func TestClient_ReadResponsesFalse_ReconnectsAfterPeerCloses(t *testing.T) {
	var accepted atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			// Simulate a peer that hangs up right after connecting.
			_ = conn.Close()
		}
	}()

	cfg := DefaultConfig()
	cfg.Mode = ModeClient
	cfg.Connect = ln.Addr().String()
	cfg.ReadResponses = false
	cfg.ReconnectWait = Duration(20 * time.Millisecond)

	c := NewClient(&cfg, slog.Default(), func(*fluxmsg.FluxMsg) {}, &MockIDGen{})
	require.NoError(t, c.Start(context.Background()))
	defer func() { _ = c.Stop() }()

	require.Eventually(t, func() bool { return accepted.Load() >= 3 }, 2*time.Second, 10*time.Millisecond,
		"the client must keep reconnecting as the peer keeps closing the connection, not dial once and get stuck")
}

// TestClient_ProcessAppliesConfiguredFraming is a regression test for a real
// bug: Client.Process wrote msg.RawPayload straight to the socket with no
// framing at all, while Server.Process (via frameOutbound) applied whatever
// framing the scenario configured. A client gear configured for
// length_prefix2 sent unframed bytes indistinguishable from delimiter framing.
func TestClient_ProcessAppliesConfiguredFraming(t *testing.T) {
	received := make(chan []byte, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, errAccept := ln.Accept()
		if errAccept != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		received <- append([]byte(nil), buf[:n]...)
	}()

	cfg := DefaultConfig()
	cfg.Mode = ModeClient
	cfg.Connect = ln.Addr().String()
	cfg.ReadResponses = false
	cfg.Framing = FramingLengthPrefix2

	c := NewClient(&cfg, slog.Default(), func(*fluxmsg.FluxMsg) {}, &MockIDGen{})
	require.NoError(t, c.Start(context.Background()))
	defer func() { _ = c.Stop() }()

	require.Eventually(t, func() bool { return c.connPtr.Load() != nil }, time.Second, 10*time.Millisecond,
		"the client never connected")

	payload := []byte("hello")
	msg := fluxmsg.New()
	msg.RawPayload = payload
	_, err = c.Process(context.Background(), msg)
	require.NoError(t, err)

	select {
	case got := <-received:
		want := append([]byte{0, byte(len(payload))}, payload...)
		assert.Equal(t, want, got, "the client must apply the configured length-prefix framing, the same way the server does")
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the message the client sent")
	}
}
