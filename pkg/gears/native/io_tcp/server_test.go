// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// newTestServer builds a Server with no listener started, for exercising
// Process, bufferMessage and flushBuffer directly against synthetic
// connections, without a real accept loop.
func newTestServer(cfg Config) *Server {
	return NewServer(&cfg, slog.Default(), func(*fluxmsg.FluxMsg) {}, &MockIDGen{})
}

// registerConn adds a synthetic connection to s, as handleConn would when a
// real client connects.
func registerConn(s *Server, id string, conn net.Conn) {
	s.conns.Store(id, &Connection{id: id, conn: conn})
	s.activeConns.Add(1)
}

// Before this fix, a message with no conn.id was handed to "the first active
// connection" the internal map happened to iterate to, which is not
// necessarily the right one when several clients are connected. It must now
// refuse to guess.
func TestProcess_NoConnID_MultipleConnections_Errors(t *testing.T) {
	s := newTestServer(DefaultConfig())
	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	defer func() { _ = a1.Close(); _ = b1.Close(); _ = a2.Close(); _ = b2.Close() }()
	registerConn(s, "conn-1", a1)
	registerConn(s, "conn-2", a2)

	msg := fluxmsg.New()
	msg.RawPayload = []byte("hello")

	res, err := s.Process(context.Background(), msg)
	require.Error(t, err, "must refuse to guess which of several connections should get an unaddressed message")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "cannot choose one")

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	assert.Empty(t, s.msgBuffer, "an ambiguous message must not be silently queued either")
}

// With exactly one connection, there is nothing ambiguous about a message
// with no conn.id: it must reach that connection directly, not the buffer.
func TestProcess_NoConnID_OneConnection_SendsDirectly(t *testing.T) {
	s := newTestServer(DefaultConfig())
	serverSide, clientSide := net.Pipe()
	defer func() { _ = serverSide.Close(); _ = clientSide.Close() }()
	registerConn(s, "conn-1", serverSide)

	msg := fluxmsg.New()
	msg.RawPayload = []byte("hello")

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := clientSide.Read(buf)
		done <- buf[:n]
	}()

	res, err := s.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.Nil(t, res)

	select {
	case got := <-done:
		assert.Equal(t, "hello", string(got))
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive the message on the sole active connection")
	}

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	assert.Empty(t, s.msgBuffer, "must not be buffered when it was sent directly")
}

// With no connections at all, a message with no conn.id is legitimately
// queued: this is the case the buffer exists for.
func TestProcess_NoConnID_ZeroConnections_Buffers(t *testing.T) {
	s := newTestServer(DefaultConfig())

	msg := fluxmsg.New()
	msg.RawPayload = []byte("hello")

	res, err := s.Process(context.Background(), msg)
	require.NoError(t, err)
	assert.Nil(t, res)

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	require.Len(t, s.msgBuffer, 1)
	assert.Equal(t, "hello", string(s.msgBuffer[0]))
}

// Before this fix, a message whose named connection had disconnected was
// silently queued for whoever connected next, which can hand it to the wrong
// client. It must be refused, the way it was before that buffering was added.
func TestProcess_UnknownConnID_Errors(t *testing.T) {
	s := newTestServer(DefaultConfig())

	msg := fluxmsg.New()
	msg.RawPayload = []byte("hello")
	msg.Metadata["conn.id"] = "gone"

	res, err := s.Process(context.Background(), msg)
	require.Error(t, err, "a message for a connection that is gone must not be silently queued for whoever connects next")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found")

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	assert.Empty(t, s.msgBuffer)
}

// The buffer is bounded by the configured cap, dropping the oldest first.
func TestBufferMessage_DropsOldestPastTheConfiguredCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBufferedMessages = 3
	s := newTestServer(cfg)

	for i := 0; i < 5; i++ {
		s.bufferMessage([]byte(fmt.Sprintf("msg-%d", i)))
	}

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	require.Len(t, s.msgBuffer, 3)
	assert.Equal(t, "msg-2", string(s.msgBuffer[0]), "the oldest must be the ones dropped")
	assert.Equal(t, "msg-4", string(s.msgBuffer[2]))
}

// A Config built without going through ParseConfig (a zero value) must still
// get a usable buffer, not one capped at zero.
func TestBufferMessage_ZeroConfigUsesTheDefaultCap(t *testing.T) {
	s := newTestServer(Config{})
	assert.Equal(t, defaultMaxBufferedMessages, s.maxBufferSize)
}

// Before this fix, flushBuffer sent only the single oldest message to a new
// connection and left the rest stranded until another connection happened to
// arrive. The whole backlog must reach the first client that connects.
func TestFlushBuffer_DeliversTheWholeBacklog(t *testing.T) {
	s := newTestServer(DefaultConfig())
	s.bufferMessage([]byte("one"))
	s.bufferMessage([]byte("two"))
	s.bufferMessage([]byte("three"))

	serverSide, clientSide := net.Pipe()
	defer func() { _ = serverSide.Close(); _ = clientSide.Close() }()

	received := make(chan string, 3)
	go func() {
		buf := make([]byte, 64)
		for i := 0; i < 3; i++ {
			n, err := clientSide.Read(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
		}
	}()

	s.flushBuffer(serverSide)

	var got []string
	for i := 0; i < 3; i++ {
		select {
		case m := <-received:
			got = append(got, m)
		case <-time.After(2 * time.Second):
			t.Fatalf("only received %d of 3 buffered messages", len(got))
		}
	}
	assert.Equal(t, []string{"one", "two", "three"}, got, "the whole backlog must reach the new connection, in order")

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	assert.Empty(t, s.msgBuffer, "the backlog must be drained, not partially stranded")
}

// A write failure partway through a flush (the connection dies mid-flush)
// must not lose what had not been sent yet: it goes back on the buffer for
// the next connection.
func TestFlushBuffer_RequeuesWhatWasNotSentOnWriteFailure(t *testing.T) {
	s := newTestServer(DefaultConfig())
	s.bufferMessage([]byte("one"))
	s.bufferMessage([]byte("two"))

	serverSide, clientSide := net.Pipe()
	require.NoError(t, clientSide.Close()) // every write on serverSide now fails
	defer func() { _ = serverSide.Close() }()

	s.flushBuffer(serverSide)

	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	require.Len(t, s.msgBuffer, 2, "both messages must be requeued, not lost, when the connection is already gone")
	assert.Equal(t, "one", string(s.msgBuffer[0]))
	assert.Equal(t, "two", string(s.msgBuffer[1]))
}
