package simple_tcp

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
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
}

type Connection struct {
	id   string
	conn net.Conn
}

func NewServer(cfg *Config, log *slog.Logger, emit func(*fluxmsg.FluxMsg), idGen sdk.IDGenerator) *Server {
	return &Server{
		config: cfg,
		log:    log.With("impl", "simple_tcp_server"),
		emit:   emit,
		idGen:  idGen,
		done:   make(chan struct{}),
	}
}

func (s *Server) Start(ctx context.Context) error {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
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

	// Spawn accept loop
	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	defer s.listener.Close()

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
	// Generate unique connection ID using Rack's EntityID service (Session Type)
	id := s.idGen.NextEntityID(idgen.EntitySession)
	connID := fmt.Sprintf("0x%x", id) // Use Hex for shorter/cleaner IDs

	c := &Connection{id: connID, conn: conn}
	s.conns.Store(connID, c)
	defer func() {
		s.conns.Delete(connID)
		conn.Close()
	}()

	s.log.Debug("connection accepted", "conn_id", connID, "remote", conn.RemoteAddr())

	scanner := bufio.NewScanner(conn)
	scanner.Split(MakeSplitter(s.config))

	for scanner.Scan() {
		data := scanner.Bytes()
		// Copy data to avoid buffer reuse issues
		payload := make([]byte, len(data))
		copy(payload, data)

		msg := fluxmsg.New()
		msg.FluxID, _ = s.idGen.NextFluxID()
		msg.TsInit = time.Now().UnixNano()
		msg.RawPayload = payload

		host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
		msg.Metadata["peer.ip"] = host
		msg.Metadata["peer.port"] = port
		msg.Metadata["flux.source"] = "simple_tcp" // Should be actual gear name
		msg.Metadata["conn.id"] = connID           // Crucial for routing back

		// TRACE Logging (Full Dump)
		if s.log.Enabled(context.Background(), logger.LevelTrace) {
			s.log.Log(context.Background(), logger.LevelTrace, "FluxMsg received",
				"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
				"ts_init", msg.TsInit,
				"metadata", fmt.Sprintf("%v", msg.Metadata),
				"payload_hex", fmt.Sprintf("0x%x", payload),
				"payload_str", string(payload),
			)
		} else if s.log.Enabled(context.Background(), slog.LevelDebug) {
			// DEBUG Logging (Summary)
			s.log.Debug("received message", "conn_id", connID, "size", len(payload))
		}

		s.emit(msg)
	}

	if err := scanner.Err(); err != nil {
		s.log.Debug("read error", "conn_id", connID, "error", err)
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
		s.listener.Close()
	}
	s.conns.Range(func(key, value any) bool {
		value.(*Connection).conn.Close()
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
