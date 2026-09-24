// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/sdk"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Server struct {
	config *Config
	log    *slog.Logger
	emit   func(*fluxmsg.FluxMsg)
	idGen  sdk.IDGenerator

	listener net.Listener
	conns    sync.Map // map[string]*Connection
	done     chan struct{}
	// draining is closed before done: it stops the accept loop without
	// closing the connections that are still finishing their work.
	draining    chan struct{}
	drainOnce   sync.Once
	stopOnce    sync.Once
	activeConns atomic.Int64

	// Message buffer for outbound traffic with no connection to reach yet:
	// nothing has connected, or the message named no conn.id at all. A
	// message whose named conn.id belongs to a connection that is gone is
	// never queued here: see Process.
	msgBuffer     [][]byte
	msgBufferMu   sync.Mutex
	maxBufferSize int

	// Telemetry
	meter       metric.Meter
	msgsIn      metric.Int64Counter
	msgsOut     metric.Int64Counter
	bytesIn     metric.Int64Counter
	bytesOut    metric.Int64Counter
	connsActive metric.Int64UpDownCounter
	connsTotal  metric.Int64Counter
	msgsDropped metric.Int64Counter
}

type Connection struct {
	id   string
	conn net.Conn
}

func NewServer(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Server {
	meter := otel.GetMeterProvider().Meter("fluxrig/gears/io_tcp")

	msgsIn, _ := meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total incoming TCP messages"))
	msgsOut, _ := meter.Int64Counter("flux.gear.messages_out", metric.WithDescription("Total outgoing TCP messages"))
	bytesIn, _ := meter.Int64Counter("flux.port.bytes_in", metric.WithDescription("Total incoming bytes"))
	bytesOut, _ := meter.Int64Counter("flux.port.bytes_out", metric.WithDescription("Total outgoing bytes"))
	connsActive, _ := meter.Int64UpDownCounter("flux.port.connections_active", metric.WithDescription("Current active TCP connections"))
	connsTotal, _ := meter.Int64Counter("flux.port.connections_total", metric.WithDescription("Total TCP connections accepted"))
	msgsDropped, _ := meter.Int64Counter("flux.gear.messages_dropped", metric.WithDescription("Outbound messages dropped: the buffer for unaddressed traffic was full"))

	maxBufferSize := cfg.MaxBufferedMessages
	if maxBufferSize <= 0 {
		maxBufferSize = defaultMaxBufferedMessages
	}

	return &Server{
		config:        cfg,
		log:           log.With("impl", "io_tcp_server"),
		emit:          emit,
		idGen:         idGen,
		done:          make(chan struct{}),
		draining:      make(chan struct{}),
		maxBufferSize: maxBufferSize,
		meter:         meter,
		msgsIn:        msgsIn,
		msgsOut:       msgsOut,
		bytesIn:       bytesIn,
		bytesOut:      bytesOut,
		connsActive:   connsActive,
		connsTotal:    connsTotal,
		msgsDropped:   msgsDropped,
	}
}

func (s *Server) Start(ctx context.Context) error {
	listenConfig := net.ListenConfig{Control: reusePortControl}

	// Resilient Bind-Retry Loop
	// Handles transient port conflicts on macOS during rapid CI cycles.
	var listener net.Listener
	var err error
	maxAttempts := 3
	for i := 1; i <= maxAttempts; i++ {
		listener, err = listenConfig.Listen(ctx, "tcp", s.config.Bind)
		if err == nil {
			break
		}
		if i < maxAttempts {
			s.log.Warn("bind failed, retrying...", "attempt", i, "addr", s.config.Bind, "error", err)
			time.Sleep(250 * time.Millisecond)
		}
	}

	if err != nil {
		return fmt.Errorf("bind failed after %d attempts: %w", maxAttempts, err)
	}

	s.log.Info("listening", "addr", s.config.Bind)
	s.listener = listener

	go s.acceptLoop(ctx)
	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	backoff := 5 * time.Millisecond

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !isActive(s.draining) || !isActive(s.done) {
				// The listener was closed on purpose. Retrying would spin on a
				// dead socket for as long as the process lives.
				return
			}
			if isActive(s.done) {
				s.log.Error("accept failed", "error", err)
				// Backoff to prevent spin loop on persistent errors (e.g. EMFILE)
				time.Sleep(backoff)
				if backoff < 1*time.Second {
					backoff *= 2
				}
			}
			continue
		}
		// Reset backoff on success
		backoff = 5 * time.Millisecond

		// Check limits
		if s.config.MaxConnections > 0 && s.activeConns.Load() >= int64(s.config.MaxConnections) {
			s.log.Warn("max connections reached, rejecting", "limit", s.config.MaxConnections, "remote", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		s.activeConns.Add(1)
		s.connsActive.Add(ctx, 1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
		))
		s.connsTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
		))
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer s.activeConns.Add(-1)
	defer s.connsActive.Add(ctx, -1, metric.WithAttributes(
		attribute.String("gear_type", "io_tcp"),
		attribute.String("mode", "server"),
	))

	// Generate persistent Connection Wrapper
	id := s.idGen.NextEntityID(idgen.EntitySession)
	connID := fmt.Sprintf("%x", id)
	connection := &Connection{id: connID, conn: conn}

	s.conns.Store(connID, connection)
	defer func() {
		s.conns.Delete(connID)
		_ = conn.Close()
	}()

	s.log.Info("connected", "remote", conn.RemoteAddr(), "conn_id", connID)

	// Flush any buffered messages to the new connection
	s.flushBuffer(conn)

	scanner := bufio.NewScanner(conn)
	scanner.Split(MakeSplitter(s.config))
	for scanner.Scan() {
		data := scanner.Bytes()
		payload := make([]byte, len(data))
		copy(payload, data) // Copy because buffer is reused

		msg := fluxmsg.New()
		msg.FluxID, _ = s.idGen.NextFluxID()
		msg.TSInit = time.Now().UnixNano()
		msg.RawPayload = payload

		msg.Metadata["conn.id"] = connID
		msg.Metadata["flux.source"] = "io_tcp_server" // Should be actual gear name
		// Extract Peer IP/Port for metadata
		host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
		msg.Metadata["peer.ip"] = host
		msg.Metadata["peer.port"] = port

		// The payload is logged only at TRACE, with card numbers masked. Below that,
		// only its size: a log is shipped to the Mixer and kept there.
		if s.log.Enabled(ctx, logger.LevelTrace) {
			s.log.Log(ctx, logger.LevelTrace, "received message",
				"flux_id", msg.FluxID.String(),
				"conn_id", connID,
				"size", len(payload),
				"payload", logger.MaskPANString(string(payload)),
			)
		} else if s.log.Enabled(ctx, slog.LevelDebug) {
			s.log.Debug("received message",
				"flux_id", msg.FluxID.String(),
				"conn_id", connID,
				"size", len(payload),
			)
		}

		s.msgsIn.Add(ctx, 1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
		))
		s.bytesIn.Add(ctx, int64(len(payload)), metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
		))

		s.emit(msg)
	}

	if err := scanner.Err(); err != nil {
		s.log.Warn("connection error", "error", err, "conn_id", connID)
	} else {
		s.log.Info("connection closed", "conn_id", connID)
	}
}

func (s *Server) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	connID, hasConnID := msg.Metadata["conn.id"]
	var conn *Connection

	if hasConnID {
		// A message that named a connection is refused, not queued, when that
		// connection is gone: buffering it here would hand it to whichever
		// unrelated client connects next, since nothing correlates a future
		// connection back to the one this message was meant for.
		val, ok := s.conns.Load(connID)
		if !ok {
			return nil, fmt.Errorf("io_tcp: connection %s not found", connID)
		}
		conn = val.(*Connection)
	} else {
		// No conn.id: there is nothing to route to specifically. Exactly one
		// active connection is unambiguous; zero means queue for whoever
		// connects next; two or more means guessing, which is refused.
		switch active := s.activeConns.Load(); {
		case active == 0:
			s.bufferMessage(msg.RawPayload)
			return nil, nil
		case active == 1:
			if c, ok := s.soleActiveConnection(); ok {
				conn = c
			} else {
				// Lost the race with a disconnect between the count above and
				// the lookup: treat it the same as zero active connections.
				s.bufferMessage(msg.RawPayload)
				return nil, nil
			}
		default:
			return nil, fmt.Errorf("io_tcp: message has no conn.id and %d connections are active, cannot choose one", active)
		}
	}

	return nil, s.writeTo(ctx, conn, msg)
}

// writeTo frames and writes msg.RawPayload to conn.
func (s *Server) writeTo(ctx context.Context, conn *Connection, msg *fluxmsg.FluxMsg) error {
	if len(msg.RawPayload) == 0 {
		return nil
	}

	if s.log.Enabled(ctx, logger.LevelTrace) {
		pathStr := "["
		for i, h := range msg.Path {
			if h != nil {
				if i > 0 {
					pathStr += ", "
				}
				pathStr += fmt.Sprintf("{g:0x%x p:0x%x t:%d}", h.GearID, h.PortID, h.TSNano)
			}
		}
		pathStr += "]"

		s.log.Log(ctx, logger.LevelTrace, "sending message",
			"flux_id", msg.FluxID.String(),
			"conn_id", conn.id,
			"payload_len", len(msg.RawPayload),
			"payload_str", logger.MaskPANString(string(msg.RawPayload)),
			"flux_path", pathStr,
		)
	} else if s.log.Enabled(ctx, slog.LevelDebug) {
		s.log.Debug("sending message", "conn_id", conn.id, "size", len(msg.RawPayload))
	}

	outbound, err := s.frameOutbound(msg.RawPayload)
	if err != nil {
		return err
	}

	if _, err := conn.conn.Write(outbound); err != nil {
		return fmt.Errorf("write error: %w", err)
	}

	s.msgsOut.Add(ctx, 1, metric.WithAttributes(
		attribute.String("gear_type", "io_tcp"),
		attribute.String("mode", "server"),
	))
	s.bytesOut.Add(ctx, int64(len(outbound)), metric.WithAttributes(
		attribute.String("gear_type", "io_tcp"),
		attribute.String("mode", "server"),
	))

	if s.config.DelimiterAppend && s.config.Framing == FramingDelimiter {
		_, _ = conn.conn.Write([]byte(s.config.Delimiter))
	}
	return nil
}

// frameOutbound applies the configured framing to payload. The one place this
// logic lives: Process, flushBuffer and soleActiveConnection's caller all
// route outbound bytes through it, so a framing fix or a new mode is made once.
func (s *Server) frameOutbound(payload []byte) ([]byte, error) {
	switch s.config.Framing {
	case FramingLengthPrefix2:
		if len(payload) > 65535 {
			return nil, fmt.Errorf("message too large for 2-byte length prefix: %d bytes", len(payload))
		}
		length := uint16(len(payload))
		outbound := []byte{byte(length >> 8), byte(length & 0xFF)}
		return append(outbound, payload...), nil
	case FramingLengthPrefix4:
		length := uint32(len(payload))
		outbound := []byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length & 0xFF)}
		return append(outbound, payload...), nil
	default:
		// Delimiter framing or no framing: send raw.
		return payload, nil
	}
}

// soleActiveConnection returns the one active connection, and false if the
// count changed since the caller checked it (zero or more than one now).
func (s *Server) soleActiveConnection() (*Connection, bool) {
	var found *Connection
	count := 0
	s.conns.Range(func(_, value any) bool {
		count++
		found = value.(*Connection)
		return count < 2 // keep counting just long enough to detect a second
	})
	if count != 1 {
		return nil, false
	}
	return found, true
}

// bufferMessage adds a message to the queue for outbound traffic that has no
// connection to reach yet. The oldest is dropped, and the drop counted, once
// the queue is full: MaxBufferedMessages bounds how much unaddressed traffic
// a Rack holds in memory while waiting for a client.
func (s *Server) bufferMessage(payload []byte) {
	if len(payload) == 0 {
		return
	}
	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()

	buf := make([]byte, len(payload))
	copy(buf, payload)

	s.msgBuffer = append(s.msgBuffer, buf)
	if over := len(s.msgBuffer) - s.maxBufferSize; over > 0 {
		s.msgBuffer = s.msgBuffer[over:]
		s.msgsDropped.Add(context.Background(), int64(over), metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
			attribute.String("reason", "buffer_full"),
		))
		s.log.Warn("outbound buffer full, dropped oldest message(s)", "dropped", over, "buffer_size", len(s.msgBuffer))
	} else {
		s.log.Debug("buffered message", "buffer_size", len(s.msgBuffer))
	}
}

// flushBuffer delivers the whole backlog to a newly-connected client, in the
// order it was queued. A write failure requeues what was not yet sent (the
// message that failed and everything after it) rather than losing it: the
// connection that failed mid-flush is probably gone, and the next one should
// still get the rest.
func (s *Server) flushBuffer(conn net.Conn) {
	s.msgBufferMu.Lock()
	pending := s.msgBuffer
	s.msgBuffer = nil
	s.msgBufferMu.Unlock()

	if len(pending) == 0 {
		return
	}
	s.log.Info("flushing buffered messages", "count", len(pending))

	for i, payload := range pending {
		outbound, err := s.frameOutbound(payload)
		if err != nil {
			s.log.Error("dropping buffered message: cannot frame it", "error", err)
			continue
		}
		if _, err := conn.Write(outbound); err != nil {
			s.log.Error("failed to flush buffered messages, requeuing the rest", "error", err, "sent", i, "remaining", len(pending)-i)
			s.requeue(pending[i:])
			return
		}
		if s.config.DelimiterAppend && s.config.Framing == FramingDelimiter {
			_, _ = conn.Write([]byte(s.config.Delimiter))
		}
	}
}

// requeue puts messages that were never sent back at the front of the
// buffer, ahead of anything queued since, bounded by the same cap
// bufferMessage enforces.
func (s *Server) requeue(payloads [][]byte) {
	s.msgBufferMu.Lock()
	defer s.msgBufferMu.Unlock()
	s.msgBuffer = append(append([][]byte(nil), payloads...), s.msgBuffer...)
	if over := len(s.msgBuffer) - s.maxBufferSize; over > 0 {
		s.msgBuffer = s.msgBuffer[over:]
		s.msgsDropped.Add(context.Background(), int64(over), metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "server"),
			attribute.String("reason", "buffer_full"),
		))
		s.log.Warn("outbound buffer full after requeue, dropped oldest message(s)", "dropped", over, "buffer_size", len(s.msgBuffer))
	}
}

// drainPollInterval is how often Drain re-checks whether the last connection
// has finished. The deadline itself comes from the caller's context.
const drainPollInterval = 25 * time.Millisecond

// Drain stops accepting new connections and waits for the ones in flight to
// finish. It does not close them: that is Stop's job, and doing it here would
// make a graceful shutdown indistinguishable from a hard one.
func (s *Server) Drain(ctx context.Context) error {
	s.drainOnce.Do(func() {
		close(s.draining)
		if s.listener != nil {
			_ = s.listener.Close()
		}
	})

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for {
		if s.activeConns.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("io_tcp: %d connection(s) still active at the drain deadline: %w",
				s.activeConns.Load(), ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stop releases the listener and every connection. It is safe to call more than
// once, and on a server that never started.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.drainOnce.Do(func() { close(s.draining) })
		close(s.done)
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.conns.Range(func(key, value any) bool {
			_ = value.(*Connection).conn.Close()
			return true
		})
	})
	return nil
}

func isActive(c chan struct{}) bool {
	select {
	case <-c:
		return false
	default:
		return true
	}
}
