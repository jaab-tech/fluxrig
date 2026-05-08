// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package io_tcp

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
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

type Client struct {
	config *Config
	log    *slog.Logger
	emit   func(*fluxmsg.FluxMsg)
	idGen  sdk.IDGenerator

	connPtr atomic.Pointer[Connection]
	done    chan struct{}

	// Telemetry
	meter       metric.Meter
	msgsIn      metric.Int64Counter
	msgsOut     metric.Int64Counter
	bytesIn     metric.Int64Counter
	bytesOut    metric.Int64Counter
	connsActive metric.Int64UpDownCounter
	connsTotal  metric.Int64Counter
}

func NewClient(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Client {
	meter := otel.GetMeterProvider().Meter("fluxrig/gears/io_tcp")

	msgsIn, _ := meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total incoming TCP messages"))
	msgsOut, _ := meter.Int64Counter("flux.gear.messages_out", metric.WithDescription("Total outgoing TCP messages"))
	bytesIn, _ := meter.Int64Counter("flux.port.bytes_in", metric.WithDescription("Total incoming bytes"))
	bytesOut, _ := meter.Int64Counter("flux.port.bytes_out", metric.WithDescription("Total outgoing bytes"))
	connsActive, _ := meter.Int64UpDownCounter("flux.port.connections_active", metric.WithDescription("Current active TCP connections"))
	connsTotal, _ := meter.Int64Counter("flux.port.connections_total", metric.WithDescription("Total TCP connections accepted"))

	return &Client{
		config:      cfg,
		log:         log.With("impl", "io_tcp_client"),
		emit:        emit,
		idGen:       idGen,
		done:        make(chan struct{}),
		meter:       meter,
		msgsIn:      msgsIn,
		msgsOut:     msgsOut,
		bytesIn:     bytesIn,
		bytesOut:    bytesOut,
		connsActive: connsActive,
		connsTotal:  connsTotal,
	}
}

func (c *Client) Start(ctx context.Context) error {
	c.log.Info("starting client", "target", c.config.Connect)
	go c.runLoop()
	return nil
}

func (c *Client) runLoop() {
	for {
		if !isActive(c.done) {
			return
		}

		conn, err := net.Dial("tcp", c.config.Connect)
		if err != nil {
			c.log.Warn("dial failed", "error", err, "retry_in", c.config.ReconnectWait)
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
	// Generate persistent Connection Wrapper
	id := c.idGen.NextEntityID(idgen.EntitySession)
	connID := fmt.Sprintf("%x", id)
	connection := &Connection{id: connID, conn: conn}

	c.connPtr.Store(connection)
	c.connsActive.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("gear_type", "io_tcp"),
		attribute.String("mode", "client"),
	))
	c.connsTotal.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("gear_type", "io_tcp"),
		attribute.String("mode", "client"),
	))

	defer func() {
		c.connPtr.Store(nil)
		c.connsActive.Add(context.Background(), -1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "client"),
		))
		_ = conn.Close()
	}()

	c.log.Info("connected", "target", c.config.Connect)

	scanner := bufio.NewScanner(conn)
	scanner.Split(MakeSplitter(c.config))
	for scanner.Scan() {
		data := scanner.Bytes()
		payload := make([]byte, len(data))
		copy(payload, data)

		msg := fluxmsg.New()
		msg.FluxID, _ = c.idGen.NextFluxID()
		msg.TsInit = time.Now().UnixNano()
		msg.RawPayload = payload

		host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
		msg.Metadata["peer.ip"] = host
		msg.Metadata["peer.port"] = port
		msg.Metadata["conn.id"] = connection.id
		msg.Metadata["flux.source"] = "io_tcp_client"

		c.msgsIn.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "client"),
		))
		c.bytesIn.Add(context.Background(), int64(len(payload)), metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "client"),
		))

		// TRACE Logging
		if c.log.Enabled(context.Background(), logger.LevelTrace) {
			c.log.Log(context.Background(), logger.LevelTrace, "received message",
				"flux_id", msg.FluxID.String(),
				"target", c.config.Connect,
				"size", len(payload),
				"payload", string(payload),
				"hex", fmt.Sprintf("%x", payload),
			)
		} else if c.log.Enabled(context.Background(), slog.LevelDebug) {
			c.log.Debug("received message",
				"target", c.config.Connect,
				"size", len(payload),
				"payload", string(payload),
				"hex", fmt.Sprintf("%x", payload),
			)
		}

		c.emit(msg)
	}

	if err := scanner.Err(); err != nil {
		c.log.Warn("connection lost", "error", err)
	} else {
		c.log.Info("connection closed by peer")
	}
}

func (c *Client) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	conn := c.connPtr.Load()
	if conn == nil {
		return nil, fmt.Errorf("client not connected")
	}

	if len(msg.RawPayload) > 0 {
		// TRACE Logging
		if c.log.Enabled(ctx, logger.LevelTrace) {
			pathStr := "["
			for i, h := range msg.Path {
				if h != nil {
					if i > 0 {
						pathStr += ", "
					}
					pathStr += fmt.Sprintf("{g:0x%x p:0x%x t:%d}", h.GearID, h.PortID, h.TsNano)
				}
			}
			pathStr += "]"

			c.log.Log(ctx, logger.LevelTrace, "sending message",
				"flux_id", msg.FluxID.String(),
				"target", c.config.Connect,
				"size", len(msg.RawPayload),
				"payload_str", string(msg.RawPayload),
				"flux_path", pathStr,
			)
		} else if c.log.Enabled(ctx, slog.LevelDebug) {
			c.log.Debug("sending message",
				"target", c.config.Connect,
				"size", len(msg.RawPayload),
				"payload", string(msg.RawPayload),
				"hex", fmt.Sprintf("%x", msg.RawPayload),
			)
		}

		_, err := conn.conn.Write(msg.RawPayload)
		if err != nil {
			return nil, fmt.Errorf("write error: %w", err)
		}

		c.msgsOut.Add(ctx, 1, metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "client"),
		))
		c.bytesOut.Add(ctx, int64(len(msg.RawPayload)), metric.WithAttributes(
			attribute.String("gear_type", "io_tcp"),
			attribute.String("mode", "client"),
		))

		if c.config.DelimiterAppend {
			_, _ = conn.conn.Write([]byte(c.config.Delimiter))
		}
	}

	return nil, nil
}

func (c *Client) Stop() error {
	close(c.done)
	conn := c.connPtr.Load()
	if conn != nil {
		_ = conn.conn.Close()
	}
	return nil
}
