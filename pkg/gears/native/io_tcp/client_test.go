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
