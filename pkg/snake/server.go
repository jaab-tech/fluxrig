// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
package snake

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Server wraps a NATS server instance for the Snake protocol.
type Server struct {
	ns *server.Server
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
	opts.JetStreamDomain = cfg.ClusterName

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

	s := &Server{ns: ns}

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
	nc, err := nats.Connect(url)
	if err != nil {
		return err
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if exists
	stream, err := js.Stream(ctx, name)
	if err == nil {
		// Update Subjects
		info, errInfo := stream.Info(ctx)
		if errInfo != nil {
			return errInfo
		}

		cfg := info.Config
		cfg.Subjects = subjects
		_, err = js.UpdateStream(ctx, cfg)
		return err
	}

	// Create
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, // Keep until limits
		Replicas:  1,
	})

	return err
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
