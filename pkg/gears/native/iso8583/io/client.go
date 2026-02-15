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
	"sync/atomic"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Client handles outbound ISO8583 connections with length-prefixed framing.
type Client struct {
	config  *Config
	log     *slog.Logger
	emit    func(*fluxmsg.FluxMsg)
	idGen   sdk.IDGenerator
	connPtr atomic.Pointer[Connection]
	done    chan struct{}

	// Metrics
	meter       metric.Meter
	msgTotal    metric.Int64Counter
	bytesTotal  metric.Int64Counter
	latency     metric.Float64Histogram
	connsActive metric.Int64UpDownCounter
	connsTotal  metric.Int64Counter
}

// NewClient creates a new ISO8583 client.
func NewClient(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Client {
	cfg.ApplyDefaults()
	meter := otel.GetMeterProvider().Meter("fluxrig/gears/iso8583")

	// Initialize Metrics
	msgTotal, _ := meter.Int64Counter("fluxrig_rack_messages_total", metric.WithDescription("Total ISO8583 messages processed"))
	bytesTotal, _ := meter.Int64Counter("fluxrig_rack_bytes_total", metric.WithDescription("Total bytes transmitted"))
	latency, _ := meter.Float64Histogram("fluxrig_rack_latency_seconds", metric.WithDescription("Internal processing latency"), metric.WithUnit("s"))
	connsActive, _ := meter.Int64UpDownCounter("fluxrig_rack_connections_active", metric.WithDescription("Current active TCP connections"))
	connsTotal, _ := meter.Int64Counter("fluxrig_rack_connections_total", metric.WithDescription("Total TCP connections accepted"))

	return &Client{
		config:      cfg,
		log:         log.With("impl", "iso8583_client"),
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

// Start initiates the connection loop.
func (c *Client) Start(_ context.Context) error {
	c.log.Info("starting client", "target", c.config.Connect)
	go c.runLoop()
	return nil
}

func (c *Client) runLoop() {
	for {
		if !isActive(c.done) {
			return
		}

		d := net.Dialer{Timeout: time.Duration(c.config.ConnectTimeout)}
		conn, err := d.Dial("tcp", c.config.Connect)
		if err != nil {
			c.log.Warn("dial failed", "error", err, "retry_in", time.Duration(c.config.ReconnectWait))
			select {
			case <-c.done:
				return
			case <-time.After(time.Duration(c.config.ReconnectWait)):
				continue
			}
		}

		c.handleConn(conn)

		// Wait before reconnecting to avoid spin loop if peer closes immediately
		select {
		case <-c.done:
			return
		case <-time.After(time.Duration(c.config.ReconnectWait)):
			continue
		}
	}
}

func (c *Client) handleConn(conn net.Conn) {
	id := c.idGen.NextEntityID(idgen.EntitySession)
	connID := fmt.Sprintf("%x", id)
	connection := &Connection{id: connID, conn: conn}

	c.connPtr.Store(connection)

	ctx := context.Background()
	c.connsTotal.Add(ctx, 1)
	c.connsActive.Add(ctx, 1)

	defer func() {
		c.connsActive.Add(ctx, -1)
		c.connPtr.Store(nil)
		_ = conn.Close()
	}()

	c.log.Info("connected", "target", c.config.Connect, "conn_id", connID)

	for {
		if !isActive(c.done) {
			return
		}

		// Set read deadline
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(c.config.ReadTimeout))); err != nil {
			c.log.Warn("failed to set read deadline", "error", err)
			return
		}

		// Read length-prefixed frame
		payload, err := c.readFrame(conn)
		if err != nil {
			if err == io.EOF {
				c.log.Info("connection closed by peer")
			} else {
				c.log.Warn("read error", "error", err)
			}
			return
		}

		// Extract Routing Header & Strip for internal use
		headerMeta := ExtractHeader(payload, c.config)
		offset := GetHeaderOffset(payload, c.config)
		innerPayload := payload[offset:]

		// Preserve Raw Header if requested (Stateless Loopback)
		if c.config.PreserveHeaders && offset > 0 {
			headerMeta["iso8583.raw_header"] = string(payload[:offset])
		}

		// Build FluxMsg
		msg := fluxmsg.New()
		msg.FluxID, _ = c.idGen.NextFluxID()
		msg.TsInit = time.Now().UnixNano()
		msg.RawPayload = innerPayload

		for k, v := range headerMeta {
			msg.Metadata[k] = v
		}

		// Heuristic validation and telemetry (using raw payload for inspection logic)
		frameInfo := c.inspect(payload, headerMeta)

		// Skip invalid frames - don't emit garbage, continue scanning for valid frames
		if !frameInfo.Valid {
			c.log.Debug("Skipping invalid frame, scanning for next valid frame",
				"remote", c.config.Connect,
				"len", len(payload),
			)
			continue
		}

		msg.Metadata["peer.ip"] = c.config.Connect
		msg.Metadata["flux.source"] = "iso8583_client"
		msg.Metadata["conn.id"] = connID
		msg.Metadata["iso8583.mti"] = frameInfo.MTI
		msg.Metadata["iso8583.bitmaps"] = fmt.Sprintf("%d", frameInfo.BitmapCount)
		msg.Metadata["iso8583.fields"] = formatFieldList(frameInfo.ActiveFields)

		// TRACE logging
		if c.log.Enabled(ctx, loggerPkg.LevelTrace) {
			c.log.Log(ctx, loggerPkg.LevelTrace, "FluxMsg received",
				"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
				"conn_id", connID,
				"mti", frameInfo.MTI,
				"variant", c.config.Variant,
				"src_id", msg.Metadata["iso8583.src_id"],
				"dst_id", msg.Metadata["iso8583.dst_id"],
				"payload_hex", fmt.Sprintf("0x%x", payload),
			)
		}

		c.emit(msg)

		// Metric: Inbound (Response from Server)
		status := "ok"
		if !frameInfo.Valid {
			status = "error"
		}
		c.msgTotal.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("direction", "inbound"),
			attribute.String("mti", frameInfo.MTI),
			attribute.String("status", status),
		))
		c.bytesTotal.Add(context.Background(), int64(len(payload)), metric.WithAttributes(
			attribute.String("direction", "inbound"),
		))
		c.latency.Record(context.Background(), time.Since(time.Unix(0, msg.TsInit)).Seconds(), metric.WithAttributes(
			attribute.String("direction", "inbound"),
			attribute.String("mti", frameInfo.MTI),
		))
	}
}

func (c *Client) readFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, c.config.FrameLengthSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}

	var length int
	var order binary.ByteOrder = binary.BigEndian
	if c.config.FrameLengthEndian == EndianLittle {
		order = binary.LittleEndian
	}

	if c.config.FrameLengthSize == 2 {
		length = int(order.Uint16(header))
	} else {
		length = int(order.Uint32(header))
	}

	if c.config.FrameIncludesHeader {
		length -= c.config.FrameLengthSize
	}

	if length <= 0 || length > 65535 {
		return nil, fmt.Errorf("invalid frame length: %d", length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}

	return payload, nil
}

// Process sends a message to the connected peer.
func (c *Client) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	start := time.Now()
	conn := c.connPtr.Load()
	if conn == nil {
		return nil, fmt.Errorf("client not connected")
	}

	if len(msg.RawPayload) == 0 {
		return msg, nil
	}

	// Prepend Variant Header if needed (Egress)
	var header []byte
	if c.config.PreserveHeaders {
		if raw, ok := msg.Metadata["iso8583.raw_header"]; ok {
			header = []byte(raw)
		}
	}

	if header == nil {
		header = BuildHeader(c.config, msg.Metadata["iso8583.src_id"], msg.Metadata["iso8583.dst_id"], len(msg.RawPayload))
	}

	fullPayload := msg.RawPayload
	if len(header) > 0 {
		fullPayload = append(header, msg.RawPayload...)
	}

	// Build frame with length prefix
	frame, err := c.buildFrame(fullPayload)
	if err != nil {
		return nil, err
	}

	// Set write deadline
	if err := conn.conn.SetWriteDeadline(time.Now().Add(time.Duration(c.config.WriteTimeout))); err != nil {
		return nil, fmt.Errorf("failed to set write deadline: %w", err)
	}

	// TRACE logging
	if c.log.Enabled(ctx, loggerPkg.LevelTrace) {
		// Layer 1.5 inspection to provide MTI/Fields in trace even on egress
		info := c.inspect(msg.RawPayload, msg.Metadata)
		c.log.Log(ctx, loggerPkg.LevelTrace, "sending message",
			"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
			"target", c.config.Connect,
			"variant", c.config.Variant,
			"mti", info.MTI,
			"fields", formatFieldList(info.ActiveFields),
			"size", len(msg.RawPayload),
			"hex", fmt.Sprintf("%x", msg.RawPayload),
		)
	}

	if _, err := conn.conn.Write(frame); err != nil {
		return nil, fmt.Errorf("write error: %w", err)
	}

	// Telemetry - Frame Sent (DEBUG per ADR 0026)
	c.log.Debug("Frame Sent",
		"len", len(fullPayload),
		"hex", fmt.Sprintf("0x%x", fullPayload[:min(64, len(fullPayload))]),
	)

	// Metric: Outbound
	mti := msg.Metadata["iso8583.mti"]
	if mti == "" {
		mti = "unknown"
	}
	c.msgTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("direction", "outbound"),
		attribute.String("mti", mti),
		attribute.String("status", "ok"),
	))
	c.bytesTotal.Add(ctx, int64(len(fullPayload)), metric.WithAttributes(
		attribute.String("direction", "outbound"),
	))
	c.latency.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("direction", "outbound"),
		attribute.String("mti", mti),
	))

	return msg, nil
}

func (c *Client) buildFrame(payload []byte) ([]byte, error) {
	payloadLen := len(payload)
	totalLen := payloadLen
	if c.config.FrameIncludesHeader {
		totalLen += c.config.FrameLengthSize
	}

	if c.config.FrameLengthSize == 2 && totalLen > math.MaxUint16 {
		return nil, fmt.Errorf("payload too large for 2-byte header: %d", totalLen)
	}

	var order binary.ByteOrder = binary.BigEndian
	if c.config.FrameLengthEndian == EndianLittle {
		order = binary.LittleEndian
	}

	frame := make([]byte, c.config.FrameLengthSize+payloadLen)
	if c.config.FrameLengthSize == 2 {
		order.PutUint16(frame[0:], uint16(totalLen)) //nolint:gosec // validated above
	} else {
		order.PutUint32(frame[0:], uint32(totalLen)) //nolint:gosec // validated above
	}
	copy(frame[c.config.FrameLengthSize:], payload)

	return frame, nil
}

// Stop closes the client connection.
func (c *Client) Stop() error {
	close(c.done)
	conn := c.connPtr.Load()
	if conn != nil {
		_ = conn.conn.Close()
	}
	return nil
}

// Drain signals the client to stop accepting new work.
// For Client, this implies we stop initiating new connections but keep existing ones open
// to receive pending responses. Upstream components (Bus) should stop calling Process.
func (c *Client) Drain(ctx context.Context) error {
	c.log.Info("Draining Client (Keep Open for Responses)")
	<-ctx.Done()
	return ctx.Err()
}

// inspect performs heuristic validation (Layer 1.5) and returns frame info.
func (c *Client) inspect(payload []byte, meta map[string]string) FrameInfo {
	info := FrameInfo{}

	if len(payload) < 4 {
		c.log.Warn("Frame too short for ISO8583", "len", len(payload))
		return info
	}

	// Skip TPDU or Custom Header
	offset := GetHeaderOffset(payload, c.config)

	// Peek MTI
	if len(payload) > offset+2 {
		info.MTI, info.MTILen = decodeMTI(payload, offset, c.config.Encoding)
	}

	// Recursive bitmap detection starting after MTI
	bitmapOffset := offset + info.MTILen
	var bitmapBytes int
	if c.config.HeuristicValidation && len(payload) > bitmapOffset+8 {
		info.BitmapCount, info.ActiveFields, bitmapBytes = extractBitmaps(payload[bitmapOffset:], c.config.Encoding)

		// Heuristic assertion: min size = MTI + bitmaps + field_count
		minSize := info.MTILen + bitmapBytes + len(info.ActiveFields)
		info.Valid = len(payload) >= minSize

		if !info.Valid {
			c.log.Warn("Frame failed heuristic validation",
				"len", len(payload),
				"min_expected", minSize,
			)
		}
	}

	// Telemetry - Log "Frame Received" at DEBUG level for valid frames (per ADR 0026)
	if info.Valid {
		args := []any{
			"len", len(payload),
			"mti", info.MTI,
			"bitmaps", info.BitmapCount,
			"fields", formatFieldList(info.ActiveFields),
		}
		c.log.Debug("Frame Received", args...)
	}

	return info
}
