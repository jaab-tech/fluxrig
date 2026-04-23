// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Server wraps a NATS server instance for the Snake protocol.
type Server struct {
	ns     *server.Server
	domain string
}

// Config holds the configuration for the Snake Server.
type Config struct {
	Port           int
	ClusterName    string
	StoreDir       string
	StreamName     string
	StreamSubjects []string
	TLSCert        string
	TLSKey         string
}

// NewServer creates and starts an embedded NATS server with JetStream enabled.
func NewServer(cfg Config) (*Server, error) {
	opts := &server.Options{
		Port:       cfg.Port,
		JetStream:  true,
		StoreDir:   cfg.StoreDir,
		ServerName: "fluxrig-mixer-embedded",
		NoSigs:     true, // FluxRig handles signals, preventing double-shutdown panic
		Debug:      true,
		Trace:      true,
	}

	// TLS Configuration
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load snake tls certs: %w", err)
		}
		opts.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.NoClientCert,
			MinVersion:   tls.VersionTLS12,
		}
		opts.TLS = true
		opts.TLSCert = cfg.TLSCert
		opts.TLSKey = cfg.TLSKey
		opts.TLSVerify = false
	}

	// JetStream Configuration
	// opts.JetStreamDomain = cfg.ClusterName // Disabled for restoration (use default)

	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}

	// Start NATS
	go ns.Start()

	// Wait for readiness
	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, errors.New("nats server failed to start")
	}

	s := &Server{ns: ns, domain: cfg.ClusterName}

	// 4. Provision Streams
	if cfg.StreamName != "" {
		if err := s.ProvisionStream(cfg.StreamName, cfg.StreamSubjects); err != nil {
			s.Shutdown()
			return nil, err
		}
	}

	return s, nil
}

// ProvisionStream checks if a stream exists and creates it if not.
func (s *Server) ProvisionStream(name string, subjects []string) error {
	// Connect to self
	url := s.ns.ClientURL()
	// Use InsecureSkipVerify for self-connection during provisioning IF TLS is enabled
	var natsOpts []nats.Option
	if strings.HasPrefix(url, "tls://") {
		// #nosec G402
		natsOpts = append(natsOpts, nats.Secure(&tls.Config{InsecureSkipVerify: true}))
	}

	// Connect to self with retry resilience (ADR 0032 follow-up)
	var nc *nats.Conn
	var err error
	for i := 1; i <= 3; i++ {
		nc, err = nats.Connect(url, natsOpts...)
		if err == nil {
			break
		}
		if i < 3 {
			slog.Info("Snake provisioning connect failed, retrying...", "attempt", i, "error", err)
			time.Sleep(250 * time.Millisecond)
		}
	}

	if err != nil {
		return fmt.Errorf("snake: failed to connect for provisioning after 3 attempts: %w", err)
	}
	defer nc.Close()

	var js jetstream.JetStream
	var jsErr error
	js, jsErr = jetstream.New(nc)

	if jsErr != nil {
		return fmt.Errorf("snake: failed to initialize jetstream: %w", jsErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if exists
	stream, err := js.Stream(ctx, name)
	if err == nil {
		// Update Subjects
		info, errInfo := stream.Info(ctx)
		if errInfo != nil {
			return fmt.Errorf("snake: failed to get stream info for %s: %w", name, errInfo)
		}

		cfg := info.Config
		cfg.Subjects = subjects
		if _, err = js.UpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("snake: failed to update stream %s subjects: %w", name, err)
		}
		slog.Info("Stream exists, subjects updated", "name", name, "subjects", subjects)
		return nil
	}

	// Create
	slog.Info("Provisioning JetStream stream", "name", name, "subjects", subjects)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, // Keep until limits
		Replicas:  1,
	})

	if err != nil {
		return fmt.Errorf("snake: failed to create stream %s: %w", name, err)
	}

	return nil
}

// Shutdown stops the embedded NATS server.
func (s *Server) Shutdown() {
	if s.ns != nil {
		s.ns.Shutdown()
		s.ns.WaitForShutdown()
	}
}

// ClientURL returns the client connection string (e.g. nats://localhost:4222).
func (s *Server) ClientURL() string {
	return s.ns.ClientURL()
}

// ClientInfo holds metrics for an individual connection.
type ClientInfo struct {
	CID      uint64
	Name     string
	IP       string
	Port     int
	InMsgs   int64
	OutMsgs  int64
	InBytes  int64
	OutBytes int64
	Uptime   string
}

// Clients returns a list of active client connections with their stats.
func (s *Server) Clients() ([]ClientInfo, error) {
	opts := &server.ConnzOptions{
		State: server.ConnOpen,
	}
	c, err := s.ns.Connz(opts)
	if err != nil {
		return nil, err
	}

	var clients []ClientInfo
	for _, conn := range c.Conns {
		clients = append(clients, ClientInfo{
			CID:      conn.Cid,
			Name:     conn.Name,
			IP:       conn.IP,
			Port:     conn.Port,
			InMsgs:   conn.InMsgs,
			OutMsgs:  conn.OutMsgs,
			InBytes:  conn.InBytes,
			OutBytes: conn.OutBytes,
			Uptime:   conn.Uptime,
		})
	}
	return clients, nil
}

// Stats returns current NATS server metrics.
func (s *Server) Stats() map[string]any {
	// 1. Basic Varz
	v, err := s.ns.Varz(nil)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	// 2. Connection Details (Connz)
	// List connected entities
	c, err := s.ns.Connz(&server.ConnzOptions{
		State: server.ConnOpen,
	})

	connectedEntities := []string{}
	if err == nil {
		for _, conn := range c.Conns {
			if conn.Name != "" {
				connectedEntities = append(connectedEntities, conn.Name)
			}
		}
	}

	return map[string]any{
		"in_msgs":            v.InMsgs,
		"out_msgs":           v.OutMsgs,
		"in_bytes":           v.InBytes,
		"out_bytes":          v.OutBytes,
		"connections":        v.Connections,
		"subscriptions":      v.Subscriptions,
		"uptime":             v.Uptime,
		"connected_entities": connectedEntities,
	}
}
