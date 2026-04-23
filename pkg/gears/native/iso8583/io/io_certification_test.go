// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIO_CertificationBlitz(t *testing.T) {
	logger := slog.Default()
	idGen, _ := idgen.New(1)

	t.Run("Server_Framing_BE_2", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Bind = "127.0.0.1:0"
		cfg.FrameLengthSize = 2
		cfg.ReadTimeout = Duration(5 * time.Second)
		cfg.WriteTimeout = Duration(5 * time.Second)
		cfg.HeuristicValidation = false

		emittedChan := make(chan *fluxmsg.FluxMsg, 1)
		srv := NewServer(&cfg, logger, func(m *fluxmsg.FluxMsg) {
			emittedChan <- m
		}, idGen)

		require.NoError(t, srv.Start(context.Background()))
		defer func() { _ = srv.Stop() }()

		addr := srv.listener.Addr().String()
		conn, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		payload := []byte("TEST-PAYLOAD")
		header := make([]byte, 2)
		binary.BigEndian.PutUint16(header, uint16(len(payload))) //nolint:gosec // payload is constant and safe

		_, err = conn.Write(append(header, payload...))
		require.NoError(t, err)

		select {
		case msg := <-emittedChan:
			assert.Equal(t, payload, msg.RawPayload)
		case <-time.After(10 * time.Second):
			t.Fatal("Server should have emitted a message")
		}
	})

	t.Run("Server_Drain_Lifecycle", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Bind = "127.0.0.1:0"
		srv := NewServer(&cfg, logger, func(m *fluxmsg.FluxMsg) {}, idGen)
		require.NoError(t, srv.Start(context.Background()))
		addr := srv.listener.Addr().String()

		conn, _ := net.Dial("tcp", addr)
		require.NotNil(t, conn)
		defer func() { _ = conn.Close() }()

		_ = srv.Stop()
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("Client_Reconnection_Logic", func(t *testing.T) {
		port := "54321"
		addr := "127.0.0.1:" + port

		cfg := DefaultConfig()
		cfg.Connect = addr
		cfg.ReconnectWait = Duration(100 * time.Millisecond)
		cfg.ReadTimeout = Duration(5 * time.Second)
		cfg.HeuristicValidation = false

		emittedChan := make(chan *fluxmsg.FluxMsg, 1)
		cli := NewClient(&cfg, logger, func(m *fluxmsg.FluxMsg) {
			emittedChan <- m
		}, idGen)
		_ = cli.Start(context.Background())
		defer func() { _ = cli.Stop() }()

		srvCfg := DefaultConfig()
		srvCfg.Bind = addr
		srvCfg.ReadTimeout = Duration(5 * time.Second)
		srvCfg.WriteTimeout = Duration(5 * time.Second)
		srvCfg.HeuristicValidation = false
		srv := NewServer(&srvCfg, logger, func(m *fluxmsg.FluxMsg) {}, idGen)
		require.NoError(t, srv.Start(context.Background()))
		defer func() { _ = srv.Stop() }()

		// Robust wait for client connection and identification
		var targetConnID string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			srv.conns.Range(func(k, v interface{}) bool {
				targetConnID = k.(string)
				return false
			})
			if targetConnID != "" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.NotEmpty(t, targetConnID, "Server should have captured client connection within 10s")

		payload := []byte("RECONNECT-TEST")
		msg := fluxmsg.New()
		msg.Metadata["conn.id"] = targetConnID
		msg.RawPayload = payload

		_, err := srv.Process(context.Background(), msg)
		require.NoError(t, err)

		select {
		case m := <-emittedChan:
			assert.Equal(t, payload, m.RawPayload)
		case <-time.After(10 * time.Second):
			t.Fatal("Client should have received message after reconnect")
		}
	})

	t.Run("Heuristic_Sanity_Inspection", func(t *testing.T) {
		cfg := &Config{
			Encoding:            EncodingBCD,
			HeuristicValidation: true,
			FrameLengthSize:     2,
		}
		srv := NewServer(cfg, logger, nil, idGen)
		assert.NotNil(t, srv)
		assert.Equal(t, 2, srv.config.FrameLengthSize) // int alignment
	})
}
