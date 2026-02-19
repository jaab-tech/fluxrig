// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockIDGen struct{}

func (m *MockIDGen) NextFluxID() (uint64, error)                { return 12345, nil }
func (m *MockIDGen) NextEntityID(etype idgen.EntityType) uint64 { return 9999 }

type MockCtx struct {
	cfg map[string]any
}

func (m *MockCtx) Config() map[string]any   { return m.cfg }
func (m *MockCtx) Context() context.Context { return context.Background() }
func (m *MockCtx) GearName() string         { return "test-gear" }
func (m *MockCtx) MachineID() uint64        { return 1 }
func (m *MockCtx) Logger() *slog.Logger     { return slog.Default() }
func (m *MockCtx) IDGen() sdk.IDGenerator   { return &MockIDGen{} }
func (m *MockCtx) Bus() bus.Bus             { return nil }
func (m *MockCtx) Manager() manager.Manager { return nil }

func TestGear_Init_Validation(t *testing.T) {
	g := &Gear{}
	ctx := &MockCtx{cfg: map[string]any{"mode": "invalid"}}

	err := g.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")

	// Valid Server
	ctx.cfg = map[string]any{"mode": "server", "bind": ":0"}
	err = g.Init(ctx)
	assert.NoError(t, err)
}

func TestGear_Loopback(t *testing.T) {
	// 1. Start Server Gear
	serverGear := &Gear{}

	// Find free port
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close() // Close so gear can bind

	ctxS := &MockCtx{cfg: map[string]any{"mode": "server", "bind": addr}}
	require.NoError(t, serverGear.Init(ctxS))

	serverMsgs := make(chan *fluxmsg.FluxMsg, 10)
	err := serverGear.Start(context.Background(), func(msg *fluxmsg.FluxMsg) {
		serverMsgs <- msg
	})
	assert.NoError(t, err)
	defer func() { _ = serverGear.Stop() }()

	// 2. Start Client Gear
	clientGear := &Gear{}
	ctxC := &MockCtx{cfg: map[string]any{"mode": "client", "connect": addr, "reconnect_wait": "100ms"}}
	require.NoError(t, clientGear.Init(ctxC))

	clientMsgs := make(chan *fluxmsg.FluxMsg, 10)
	err = clientGear.Start(context.Background(), func(msg *fluxmsg.FluxMsg) {
		clientMsgs <- msg
	})
	assert.NoError(t, err)
	defer func() { _ = clientGear.Stop() }()

	// 3. Test Flow
	// Wait for connection
	time.Sleep(200 * time.Millisecond)

	// A. Client sends to Server (via Egress)
	// Client Gear is in "Client Mode". Logic:
	// - Start(): Dials Server. Reads from Socket -> Emits.
	// - Process(): Writes to Socket.

	// We want to send data TO the server. So we call clientGear.Process().
	msgOut := fluxmsg.New()
	msgOut.RawPayload = []byte("hello server\n")
	_, err = clientGear.Process(context.Background(), msgOut)
	assert.NoError(t, err)

	// Verify Server Received and Emitted
	select {
	case msg := <-serverMsgs:
		assert.Equal(t, "hello server", string(msg.RawPayload))
		assert.Equal(t, "io_tcp_server", msg.Metadata["flux.source"])
	case <-time.After(1 * time.Second):
		t.Fatal("Server did not receive message")
	}

	// 2. Server sends to Client (via Egress)
	// serverGear.Process() must target the correct connection.

	// We need the connID from the received message to reply.
	// connID := msg.GetHeader("conn_id")
}

func TestGear_E2E_RoundTrip(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String() // Capture address before closing
	_ = l.Close()

	// --- SETUP SERVER ---
	s := &Gear{}
	require.NoError(t, s.Init(&MockCtx{cfg: map[string]any{"mode": "server", "bind": addr}}))

	sReceived := make(chan *fluxmsg.FluxMsg, 5)
	assert.NoError(t, s.Start(context.Background(), func(m *fluxmsg.FluxMsg) { sReceived <- m }))
	defer func() { _ = s.Stop() }()

	// --- SETUP CLIENT ---
	c := &Gear{}
	require.NoError(t, c.Init(&MockCtx{cfg: map[string]any{"mode": "client", "connect": addr, "reconnect_wait": "10ms"}}))

	cReceived := make(chan *fluxmsg.FluxMsg, 5)
	assert.NoError(t, c.Start(context.Background(), func(m *fluxmsg.FluxMsg) { cReceived <- m }))
	defer func() { _ = c.Stop() }()

	time.Sleep(100 * time.Millisecond) // Allow connect

	// 1. Client -> Server
	// Client Gear Process() writes to the socket.
	_, err = c.Process(context.Background(), &fluxmsg.FluxMsg{RawPayload: []byte("ping\n")})
	assert.NoError(t, err)

	// Check Server emitted it
	var connID string
	select {
	case msg := <-sReceived:
		assert.Equal(t, "ping", string(msg.RawPayload))
		connID = msg.Metadata["conn.id"]
		assert.NotEmpty(t, connID)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for ping")
	}

	// 2. Server -> Client (Reply)
	// Server Gear Process() writes back to the specific connection
	reply := &fluxmsg.FluxMsg{
		RawPayload: []byte("pong\n"),
		Metadata:   map[string]string{"conn.id": connID},
	}
	_, err = s.Process(context.Background(), reply)
	assert.NoError(t, err)

	// Check Client emitted it (Client reads from socket -> emits)
	select {
	case msg := <-cReceived:
		assert.Equal(t, "pong", string(msg.RawPayload))
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for pong")
	}
}
