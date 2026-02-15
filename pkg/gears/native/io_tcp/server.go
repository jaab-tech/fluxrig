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
)

type Server struct {
	config *Config
	log    *slog.Logger
	emit   func(*fluxmsg.FluxMsg)
	idGen  sdk.IDGenerator

	listener net.Listener
	conns    sync.Map // map[string]*Connection
	done     chan struct{}
	activeConns atomic.Int64
}

type Connection struct {
	id   string
	conn net.Conn
}

func NewServer(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Server {
	return &Server{
		config: cfg,
		log:    log.With("impl", "io_tcp_server"),
		emit:   emit,
		idGen:  idGen,
		done:   make(chan struct{}),
	}
}

func (s *Server) Start(ctx context.Context) error {
	listenConfig := net.ListenConfig{Control: reusePortControl}
	listener, err := listenConfig.Listen(ctx, "tcp", s.config.Bind)
	if err != nil {
		return fmt.Errorf("bind failed: %w", err)
	}

	s.log.Info("listening", "addr", s.config.Bind)
	s.listener = listener

	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	backoff := 5 * time.Millisecond

	for {
		conn, err := s.listener.Accept()
		if err != nil {
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
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.activeConns.Add(-1)

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

	scanner := bufio.NewScanner(conn)
	scanner.Split(MakeSplitter(s.config))
	for scanner.Scan() {
		data := scanner.Bytes()
		payload := make([]byte, len(data))
		copy(payload, data) // Copy because buffer is reused

		msg := fluxmsg.New()
		msg.FluxID, _ = s.idGen.NextFluxID()
		msg.TsInit = time.Now().UnixNano()
		msg.RawPayload = payload

		msg.Metadata["conn.id"] = connID
		msg.Metadata["flux.source"] = "io_tcp_server" // Should be actual gear name
		// Extract Peer IP/Port for metadata
		host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
		msg.Metadata["peer.ip"] = host
		msg.Metadata["peer.port"] = port

		// TRACE Logging
		// Use loggerpkg.LevelTrace if available, or Debug
		if s.log.Enabled(context.Background(), logger.LevelTrace) { // Changed loggerPkg.LevelTrace to logger.LevelTrace
			s.log.Log(context.Background(), logger.LevelTrace, "received message",
				"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
				"conn_id", connID,
				"size", len(payload),
				"payload", string(payload),
				"hex", fmt.Sprintf("%x", payload),
			)
		} else if s.log.Enabled(context.Background(), slog.LevelDebug) {
			s.log.Debug("received message",
				"conn_id", connID,
				"size", len(payload),
				"payload", string(payload),
				"hex", fmt.Sprintf("%x", payload),
			)
		}

		s.emit(msg)
	}

	if err := scanner.Err(); err != nil {
		s.log.Warn("connection error", "error", err, "conn_id", connID)
	} else {
		s.log.Info("connection closed", "conn_id", connID)
	}
}

func (s *Server) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	connID, ok := msg.Metadata["conn.id"]
	if !ok {
		return nil, fmt.Errorf("missing conn.id in metadata")
	}

	val, ok := s.conns.Load(connID)
	if !ok {
		return nil, fmt.Errorf("connection %s not found", connID)
	}

	conn := val.(*Connection)

	// Write RawPayload back
	if len(msg.RawPayload) > 0 {
		// TRACE Logging
		if s.log.Enabled(ctx, logger.LevelTrace) {
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

			s.log.Log(ctx, logger.LevelTrace, "sending message",
				"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
				"conn_id", connID,
				"payload_len", len(msg.RawPayload),
				"payload_str", string(msg.RawPayload),
				"flux_path", pathStr,
			)
		} else if s.log.Enabled(ctx, slog.LevelDebug) {
			s.log.Debug("sending message",
				"conn_id", connID,
				"size", len(msg.RawPayload),
			)
		}

		_, err := conn.conn.Write(msg.RawPayload)
		if err != nil {
			return nil, fmt.Errorf("write error: %w", err)
		}

		if s.config.DelimiterAppend {
			_, _ = conn.conn.Write([]byte(s.config.Delimiter))
		}
	}

	return msg, nil
}

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

func isActive(c chan struct{}) bool {
	select {
	case <-c:
		return false
	default:
		return true
	}
}
