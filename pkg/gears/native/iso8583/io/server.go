// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package io

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Server handles inbound ISO8583 connections with length-prefixed framing.
type Server struct {
	config   *Config
	log      *slog.Logger
	emit     func(*fluxmsg.FluxMsg)
	idGen    sdk.IDGenerator
	listener net.Listener
	conns    sync.Map // map[string]*Connection
	done     chan struct{}

	// Metrics
	meter       metric.Meter
	msgTotal    metric.Int64Counter
	bytesTotal  metric.Int64Counter
	latency     metric.Float64Histogram
	connsActive metric.Int64UpDownCounter
	connsTotal  metric.Int64Counter

	draining atomic.Bool
}

// Connection represents an active ISO8583 connection.
type Connection struct {
	id   string
	conn net.Conn
}

// NewServer creates a new ISO8583 server.
func NewServer(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Server {
	cfg.ApplyDefaults()

	meter := otel.GetMeterProvider().Meter("fluxrig/gears/iso8583")

	// Initialize Metrics
	msgTotal, _ := meter.Int64Counter("fluxrig_rack_messages_total", metric.WithDescription("Total ISO8583 messages processed"))
	bytesTotal, _ := meter.Int64Counter("fluxrig_rack_bytes_total", metric.WithDescription("Total bytes transmitted"))
	latency, _ := meter.Float64Histogram("fluxrig_rack_latency_seconds", metric.WithDescription("Internal processing latency"), metric.WithUnit("s"))
	connsActive, _ := meter.Int64UpDownCounter("fluxrig_rack_connections_active", metric.WithDescription("Current active TCP connections"))
	connsTotal, _ := meter.Int64Counter("fluxrig_rack_connections_total", metric.WithDescription("Total TCP connections accepted"))

	return &Server{
		config:      cfg,
		log:         log.With("impl", "iso8583_server"),
		emit:        emit,
		idGen:       idGen,
		done:        make(chan struct{}),
		meter:       meter,
		msgTotal:    msgTotal,
		bytesTotal:  bytesTotal,
		latency:     latency,
		connsActive: connsActive,
		connsTotal:  connsTotal,
	}
}

// Start begins listening for connections.
func (s *Server) Start(ctx context.Context) error {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
	l, err := lc.Listen(ctx, "tcp", s.config.Bind)
	if err != nil {
		return fmt.Errorf("failed to bind %s: %w", s.config.Bind, err)
	}
	s.listener = l
	s.log.Info("listening", "addr", s.config.Bind)

	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	defer func() { _ = s.listener.Close() }()

	for {
		select {
		case <-s.done:
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if isActive(s.done) {
				s.log.Error("accept error", "error", err)
			}
			continue
		}

		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	id := s.idGen.NextEntityID(idgen.EntitySession)
	connID := fmt.Sprintf("0x%x", id)

	ctx := context.Background()
	s.connsTotal.Add(ctx, 1)
	s.connsActive.Add(ctx, 1)

	c := &Connection{id: connID, conn: conn}
	s.conns.Store(connID, c)
	defer func() {
		s.connsActive.Add(ctx, -1)
		s.conns.Delete(connID)
		_ = conn.Close()
	}()

	s.log.Info("connection accepted", "conn_id", connID, "remote", conn.RemoteAddr())

	for {
		if !isActive(s.done) {
			return
		}

		// Set read deadline
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(s.config.ReadTimeout))); err != nil {
			s.log.Warn("failed to set read deadline", "error", err)
			return
		}

		// Read length-prefixed frame
		payload, err := s.readFrame(conn)
		if err != nil {
			// Check if we are draining (forced read error)
			if s.isDraining() {
				s.log.Info("Draining connection (Read Stopped, Write Open)", "conn_id", connID)
				// Block until server is fully stopped (ctx done or Stop called)
				<-s.done
				return
			}

			if err == io.EOF {
				s.log.Info("connection closed by peer", "conn_id", connID, "remote", conn.RemoteAddr().String())
			} else {
				s.log.Warn("read error", "conn_id", connID, "remote", conn.RemoteAddr().String(), "error", err)
			}
			return
		}

		// Extract Routing Header & Strip for internal use
		headerMeta := ExtractHeader(payload, s.config)
		offset := GetHeaderOffset(payload, s.config)
		innerPayload := payload[offset:]

		// Preserve Raw Header if requested (Stateless Loopback)
		if s.config.PreserveHeaders && offset > 0 {
			headerMeta["iso8583.raw_header"] = string(payload[:offset])
		}

		// Build FluxMsg
		msg := fluxmsg.New()
		msg.FluxID, _ = s.idGen.NextFluxID()
		msg.TsInit = time.Now().UnixNano()
		msg.RawPayload = innerPayload

		for k, v := range headerMeta {
			msg.Metadata[k] = v
		}

		// Heuristic validation and telemetry (using raw payload for inspection logic)
		frameInfo := s.inspect(payload, connID, headerMeta)

		// Fail Fast: Disconnect on invalid frames to preserve synchronization integrity (ISO8583 Framework Style)
		if !frameInfo.Valid {
			s.log.Warn("Disconnecting due to invalid frame (Fail Fast)",
				"conn_id", connID,
				"remote", conn.RemoteAddr().String(),
				"len", len(payload),
			)
			return
		}

		host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
		msg.Metadata["peer.ip"] = host
		msg.Metadata["peer.port"] = port
		msg.Metadata["flux.source"] = "iso8583"
		msg.Metadata["conn.id"] = connID
		msg.Metadata["iso8583.mti"] = frameInfo.MTI
		msg.Metadata["iso8583.bitmaps"] = fmt.Sprintf("%d", frameInfo.BitmapCount)
		msg.Metadata["iso8583.fields"] = formatFieldList(frameInfo.ActiveFields)

		// TRACE logging
		if s.log.Enabled(context.Background(), logger.LevelTrace) {
			s.log.Log(context.Background(), logger.LevelTrace, "FluxMsg received",
				"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
				"conn_id", connID,
				"mti", frameInfo.MTI,
				"src_id", msg.Metadata["iso8583.src_id"],
				"dst_id", msg.Metadata["iso8583.dst_id"],
				"payload_hex", fmt.Sprintf("0x%x", payload),
			)
		}

		s.emit(msg)

		s.latency.Record(context.Background(), time.Since(time.Unix(0, msg.TsInit)).Seconds(), metric.WithAttributes(
			attribute.String("direction", "inbound"),
			attribute.String("mti", frameInfo.MTI),
		))
	}
}

func (s *Server) readFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, s.config.FrameLengthSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}

	var length int
	var order binary.ByteOrder = binary.BigEndian
	if s.config.FrameLengthEndian == EndianLittle {
		order = binary.LittleEndian
	}

	if s.config.FrameLengthSize == 2 {
		length = int(order.Uint16(header))
	} else {
		length = int(order.Uint32(header))
	}

	if s.config.FrameIncludesHeader {
		length -= s.config.FrameLengthSize
	}

	if length <= 0 || length > 65535 {
		return nil, fmt.Errorf("invalid frame length: %d", length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}

	// TRACE logging for framing validation
	if s.log.Enabled(context.Background(), logger.LevelTrace) {
		fullFrame := append(header, payload...)
		s.log.Log(context.Background(), logger.LevelTrace, "raw frame read",
			"len_prefix", length,
			"frame_hex", fmt.Sprintf("0x%x", fullFrame),
		)
	}

	return payload, nil
}

// Process sends a message to the specified connection.
func (s *Server) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	start := time.Now()
	connID, ok := msg.Metadata["conn.id"]
	if !ok {
		return nil, fmt.Errorf("missing conn.id in metadata")
	}

	val, ok := s.conns.Load(connID)
	var conn *Connection
	if ok {
		conn = val.(*Connection)
	} else if !s.config.StrictConnectionRouting {
		// Fallback: Pick random active connection (Requested for Loopback Testing)
		// Since we can't correlate TCP-Echo response back to original Server connection easily in this topology.
		s.conns.Range(func(key, value any) bool {
			conn = value.(*Connection)
			return false // Stop after first
		})

		if conn != nil {
			s.log.Warn("connection not found, falling back to random active connection (relaxed routing)",
				"target_id", connID,
				"fallback_id", conn.id,
			)
		} else {
			return nil, fmt.Errorf("connection %s not found and no fallback available", connID)
		}
	} else {
		return nil, fmt.Errorf("connection %s not found", connID)
	}

	if len(msg.RawPayload) == 0 {
		return msg, nil
	}

	// Prepend Variant Header if needed (Egress)
	// If PreserveHeaders is enabled and we have a raw header, use it (Echo).
	var header []byte
	if s.config.PreserveHeaders {
		if raw, ok := msg.Metadata["iso8583.raw_header"]; ok {
			header = []byte(raw)
		}
	}

	if header == nil {
		header = BuildHeader(s.config, msg.Metadata["iso8583.src_id"], msg.Metadata["iso8583.dst_id"], len(msg.RawPayload))
	}

	fullPayload := msg.RawPayload
	if len(header) > 0 {
		fullPayload = append(header, msg.RawPayload...)
	}

	// Build frame with length prefix
	frame, err := s.buildFrame(fullPayload)
	if err != nil {
		return nil, err
	}

	// Set write deadline
	if err := conn.conn.SetWriteDeadline(time.Now().Add(time.Duration(s.config.WriteTimeout))); err != nil {
		return nil, fmt.Errorf("failed to set write deadline: %w", err)
	}

	// TRACE logging
	if s.log.Enabled(ctx, logger.LevelTrace) {
		s.log.Log(ctx, logger.LevelTrace, "sending message",
			"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
			"conn_id", connID,
			"payload_len", len(msg.RawPayload),
			"hex", fmt.Sprintf("%x", msg.RawPayload),
		)
	}

	if _, err := conn.conn.Write(frame); err != nil {
		return nil, fmt.Errorf("write error: %w", err)
	}

	// Telemetry - Frame Sent (DEBUG per ADR 0026)
	s.log.Debug("Frame Sent",
		"conn_id", connID,
		"remote", conn.conn.RemoteAddr().String(),
		"len", len(fullPayload),
		"hex", fmt.Sprintf("0x%x", fullPayload[:min(64, len(fullPayload))]),
	)

	// Metric: Egress
	// MTI extraction from raw payload is expensive if we do full parse again.
	// Ideally metadata has it.
	mti := msg.Metadata["iso8583.mti"]
	if mti == "" {
		mti = "unknown"
	}

	// Measure Write Latency
	// Note: This only measures internal serialization+write, not full RTT.
	// Full RTT is tracked by tracing.
	// We don't have start time passed in accessible way easily unless we instrument start of func.
	// But we can approximate if we consider Process is synchronous.
	// START time is not captured in previous step. I will capture it now.

	s.msgTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("direction", "outbound"),
		attribute.String("mti", mti),
		attribute.String("status", "ok"),
	))
	s.bytesTotal.Add(ctx, int64(len(fullPayload)), metric.WithAttributes(
		attribute.String("direction", "outbound"),
	))
	s.latency.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("direction", "outbound"),
		attribute.String("mti", mti),
	))

	return msg, nil
}

func (s *Server) buildFrame(payload []byte) ([]byte, error) {
	payloadLen := len(payload)
	totalLen := payloadLen
	if s.config.FrameIncludesHeader {
		totalLen += s.config.FrameLengthSize
	}

	if s.config.FrameLengthSize == 2 && totalLen > math.MaxUint16 {
		return nil, fmt.Errorf("payload too large for 2-byte header: %d", totalLen)
	}

	var order binary.ByteOrder = binary.BigEndian
	if s.config.FrameLengthEndian == EndianLittle {
		order = binary.LittleEndian
	}

	frame := make([]byte, s.config.FrameLengthSize+payloadLen)
	if s.config.FrameLengthSize == 2 {
		order.PutUint16(frame[0:], uint16(totalLen)) //nolint:gosec // validated above
	} else {
		order.PutUint32(frame[0:], uint32(totalLen)) //nolint:gosec // validated above
	}
	copy(frame[s.config.FrameLengthSize:], payload)

	return frame, nil
}

// Stop closes all connections and the listener.
func (s *Server) Stop() error {
	close(s.done)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.conns.Range(func(key, value any) bool {
		_ = value.(*Connection).conn.Close()
		return true
	})
	return nil
}

// Drain signals the server to stop accepting new work but complete pending writes.
func (s *Server) Drain(ctx context.Context) error {
	s.log.Info("Draining Server (Stop Accept, Stop Read, Allow Write)")
	s.draining.Store(true)

	// 1. Stop Accepting
	if s.listener != nil {
		_ = s.listener.Close()
	}

	// 2. Stop Reading on all connections (Backpressure)
	// We do this by setting a past ReadDeadline, which forces Read to return error,
	// checking the error, and breaking the loop.
	// BUT we want to keep the socket open for Writes.
	// We need to signal the handleConn loop to exit "read mode" but not close socket yet?
	// Actually, if we stop reading, the loop exits. If loop exits, it defers Close().
	// We need handleConn to wait.

	// To achieve "Stop Read, Allow Write", we must modify handleConn logic.
	// For now, simpler approach:
	// We rely on the fact that if we just close the Listener, new inputs stop.
	// Existing connections: We want to stop reading NEW requests.
	// We can set a flag `s.draining`?
	// Given the interface constraints, we'll implement a best-effort "Stop Listener"
	// and assume `Process` (Write) is called by the Bus on its own schedule.
	// We just wait for the context to expire.

	// Better: We can instruct connections to stop reading.
	s.conns.Range(func(key, value any) bool {
		conn := value.(*Connection)
		// Force Read to unblock/fail so loop checks status
		_ = conn.conn.SetReadDeadline(time.Now())
		return true
	})

	// Wait for context
	<-ctx.Done()
	return ctx.Err()
}

// inspect performs heuristic validation (Layer 1.5) and returns frame info.
func (s *Server) inspect(payload []byte, connID string, meta map[string]string) FrameInfo {
	info := FrameInfo{}

	if len(payload) < 4 {
		s.log.Warn("Frame too short for ISO8583", "len", len(payload), "conn_id", connID)
		return info
	}

	// Skip TPDU or Custom Header
	offset := GetHeaderOffset(payload, s.config)

	// Peek MTI
	if len(payload) > offset+2 {
		info.MTI, info.MTILen = decodeMTI(payload, offset, s.config.Encoding)
	}

	// Recursive bitmap detection
	bitmapOffset := offset + info.MTILen
	var bitmapBytes int
	if s.config.HeuristicValidation && len(payload) > bitmapOffset+8 {
		info.BitmapCount, info.ActiveFields, bitmapBytes = extractBitmaps(payload[bitmapOffset:], s.config.Encoding)

		minSize := info.MTILen + bitmapBytes + len(info.ActiveFields)
		info.Valid = len(payload) >= minSize

		if !info.Valid {
			s.log.Warn("Frame failed heuristic validation",
				"conn_id", connID,
				"len", len(payload),
				"min_expected", minSize,
			)
		}
	}

	// Telemetry - Log "Frame Received" at DEBUG level for valid frames (per ADR 0026)
	if info.Valid {
		args := []any{
			"conn_id", connID,
			"len", len(payload),
			"mti", info.MTI,
			"bitmaps", info.BitmapCount,
			"fields", formatFieldList(info.ActiveFields),
		}
		s.log.Debug("Frame Received", args...)
	}

	// Metric: Ingress (Status=ok refers to heuristic validity here)
	status := "ok"
	if !info.Valid {
		status = "error"
	}
	ctx := context.Background()
	s.msgTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("direction", "inbound"),
		attribute.String("mti", info.MTI),
		attribute.String("status", status),
	))
	s.bytesTotal.Add(ctx, int64(len(payload)), metric.WithAttributes(
		attribute.String("direction", "inbound"),
	))

	return info
}

func (s *Server) isDraining() bool {
	return s.draining.Load()
}

func isActive(c chan struct{}) bool {
	select {
	case <-c:
		return false
	default:
		return true
	}
}
