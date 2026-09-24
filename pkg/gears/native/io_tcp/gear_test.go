// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

type MockIDGen struct{}

func (m *MockIDGen) NextFluxID() (uuid.UUID, error)                { return uuid.New(), nil }
func (m *MockIDGen) NextEntityID(etype idgen.EntityType) uuid.UUID { return uuid.New() }

type MockCtx struct {
	cfg map[string]any
	log *slog.Logger // nil means the default logger
}

func (m *MockCtx) Config() map[string]any   { return m.cfg }
func (m *MockCtx) Context() context.Context { return context.Background() }
func (m *MockCtx) GearName() string         { return "test-gear" }
func (m *MockCtx) MachineID() uuid.UUID     { return uuid.Nil }
func (m *MockCtx) Logger() *slog.Logger {
	if m.log != nil {
		return m.log
	}
	return slog.Default()
}
func (m *MockCtx) IDGen() sdk.IDGenerator   { return &MockIDGen{} }
func (m *MockCtx) Bus() bus.Bus             { return nil }
func (m *MockCtx) Manager() manager.Manager { return nil }
func (m *MockCtx) ControlPlane() any        { return nil }
func (m *MockCtx) ClusterPublicKey() []byte { return nil }
func (m *MockCtx) Emitter() sdk.PortEmitter { return sdk.NewNoopEmitter() }

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

// A drain that reports failure on every orderly shutdown is worse than no
// drain: the runtime logs "Drain Gear Failed" and collects an error for what
// was a clean stop. This asserts the three properties that were wrong.
func TestServerDrain(t *testing.T) {
	t.Run("returns nil when nothing is connected", func(t *testing.T) {
		s := NewServer(&Config{Bind: "127.0.0.1:0"}, slog.Default(), func(*fluxmsg.FluxMsg) {}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() { _ = s.Stop() }()

		if err := s.Drain(ctx); err != nil {
			t.Fatalf("drain of an idle server reported failure: %v", err)
		}
	})

	t.Run("stops accepting once drained", func(t *testing.T) {
		s := NewServer(&Config{Bind: "127.0.0.1:0"}, slog.Default(), func(*fluxmsg.FluxMsg) {}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := s.listener.Addr().String()
		defer func() { _ = s.Stop() }()

		if err := s.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
			_ = c.Close()
			t.Fatal("the listener still accepted a connection after Drain")
		}
	})

	t.Run("Stop is idempotent", func(t *testing.T) {
		s := NewServer(&Config{Bind: "127.0.0.1:0"}, slog.Default(), func(*fluxmsg.FluxMsg) {}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := s.Stop(); err != nil {
			t.Fatalf("first stop: %v", err)
		}
		// Before sync.Once this closed an already-closed channel and panicked.
		if err := s.Stop(); err != nil {
			t.Fatalf("second stop: %v", err)
		}
	})
}

// lockedBuffer is a log sink both gears write to from their own goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// exchangeCardNumber sends a message carrying testPAN from a client gear to a server
// gear and back, with both gears logging to one sink at the given level, and returns
// everything they logged.
func exchangeCardNumber(t *testing.T, level slog.Level, testPAN string) string {
	t.Helper()
	sink := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: level}))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	_ = l.Close()

	s := &Gear{}
	require.NoError(t, s.Init(&MockCtx{log: log, cfg: map[string]any{"mode": "server", "bind": addr}}))
	sReceived := make(chan *fluxmsg.FluxMsg, 5)
	require.NoError(t, s.Start(context.Background(), func(m *fluxmsg.FluxMsg) { sReceived <- m }))
	defer func() { _ = s.Stop() }()

	c := &Gear{}
	require.NoError(t, c.Init(&MockCtx{log: log, cfg: map[string]any{"mode": "client", "connect": addr, "reconnect_wait": "10ms"}}))
	cReceived := make(chan *fluxmsg.FluxMsg, 5)
	require.NoError(t, c.Start(context.Background(), func(m *fluxmsg.FluxMsg) { cReceived <- m }))
	defer func() { _ = c.Stop() }()

	require.Eventually(t, func() bool {
		_, errSend := c.Process(context.Background(), &fluxmsg.FluxMsg{RawPayload: []byte("0100" + testPAN + "\n")})
		if errSend != nil {
			return false
		}
		select {
		case <-sReceived:
			return true
		case <-time.After(50 * time.Millisecond):
			return false
		}
	}, 5*time.Second, 20*time.Millisecond, "the server never received the message")
	return sink.String()
}

// A log is shipped to the Mixer and kept there, so a card number in a message never
// reaches one at a level that is on by default or in a debugging session.
func TestGear_LogsNeverCarryTheCardNumberBelowTrace(t *testing.T) {
	const testPAN = "4111111111111111"

	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		logged := exchangeCardNumber(t, level, testPAN)
		assert.NotContains(t, logged, testPAN, "level %v", level)
		assert.NotContains(t, logged, "34313131313131313131313131313131", "the number as hex, level %v", level)
	}
}

func TestGear_TraceLogsCarryTheCardNumberMasked(t *testing.T) {
	const testPAN = "4111111111111111"

	logged := exchangeCardNumber(t, logger.LevelTrace, testPAN)

	assert.Contains(t, logged, "received message", "the trace line is there")
	assert.NotContains(t, logged, testPAN)
	assert.Contains(t, logged, "411111******1111", "first six and last four stay")
}
