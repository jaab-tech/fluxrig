// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

	// Limits applied to every stream this server provisions. Zero means none.
	streamMaxAge   time.Duration
	streamMaxBytes int64

	// Limits applied to every KV bucket this server provisions. Zero means
	// none for kvTTL (keys never expire); kvMaxBytes/kvMaxValueSize follow
	// NATS's own convention where zero is also "unlimited" for that field.
	kvMaxBytes     int64
	kvMaxValueSize int32
	kvTTL          time.Duration
}

// storeEncryptedMarker is left in the store directory once a store has been
// encrypted. Started without a key, the server would open an encrypted store and
// find it empty; the marker turns that into an error instead.
const storeEncryptedMarker = ".store-encrypted"

// Store ciphers a Config can name.
const (
	CipherChaCha = "chacha" // ChaCha20-Poly1305, the default
	CipherAES    = "aes"    // AES-GCM
)

// Config holds the configuration for the Snake Server.
type Config struct {
	Port           int
	ClusterName    string
	StoreDir       string
	StreamName     string
	StreamSubjects []string
	TLSCert        string
	TLSKey         string
	TLSCA          string
	TLSVerify      bool
	AllowNonTLS    bool
	LogLevel       string

	// StoreKey encrypts the JetStream store on disk when it is not empty: every
	// message, stream and key-value bucket. The server keeps the key and decrypts on
	// read, so this protects the files, a copy of the directory and a backup. It does
	// not protect against a NATS client, or against someone with the running process.
	// A store that already holds unencrypted data is converted on the first start.
	StoreKey string
	// StoreOldKey is the key the store was encrypted with before StoreKey, for the
	// start that rotates the key.
	StoreOldKey string
	// StoreCipher is CipherChaCha (the default) or CipherAES.
	StoreCipher string

	// StreamMaxAge and StreamMaxBytes limit every stream the server provisions.
	// Zero means no limit.
	StreamMaxAge   time.Duration
	StreamMaxBytes int64

	// KVMaxBytes, KVMaxValueSize and KVTTL limit every KV bucket the server
	// provisions, the same way StreamMaxAge/StreamMaxBytes limit streams.
	KVMaxBytes     int64
	KVMaxValueSize int32
	KVTTL          time.Duration
}

// NewServer creates and starts an embedded NATS server with JetStream enabled.
func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	opts := &server.Options{
		Port:       cfg.Port,
		JetStream:  true,
		StoreDir:   cfg.StoreDir,
		ServerName: "fluxrig-mixer-embedded",
		NoSigs:     true, // fluxrig handles signals, preventing double-shutdown panic
		HTTPPort:   0,    // Disable HTTP for security/simplicity
	}

	// A store that was encrypted must not be opened without its key.
	markerPath := filepath.Join(cfg.StoreDir, storeEncryptedMarker)
	if cfg.StoreKey == "" {
		if _, errMarker := os.Stat(markerPath); errMarker == nil {
			return nil, fmt.Errorf("snake: the store in %s was encrypted and no store key is set: set the key again, or move the directory away to start an empty store", cfg.StoreDir)
		}
	}

	// Encryption at rest
	if cfg.StoreKey != "" {
		opts.JetStreamKey = cfg.StoreKey
		opts.JetStreamOldKey = cfg.StoreOldKey
		switch strings.ToLower(cfg.StoreCipher) {
		case "", CipherChaCha:
			opts.JetStreamCipher = server.ChaCha
		case CipherAES:
			opts.JetStreamCipher = server.AES
		default:
			return nil, fmt.Errorf("snake: unknown store cipher %q (use %q or %q)", cfg.StoreCipher, CipherChaCha, CipherAES)
		}
	}

	// Map LogLevel to NATS Debug/Trace
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		opts.Debug = true
	case "trace":
		opts.Debug = true
		opts.Trace = true
	}

	// Internal Optimization: Use Unix Socket for Mixer-to-Snake communication
	// This bypasses TLS and network stack entirely.
	unixPath := filepath.Join(cfg.StoreDir, "snake.sock")
	_ = os.Remove(unixPath) // Clean up old socket
	// opts.UnixSocket = unixPath // NATS server options for Unix Socket

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

		if cfg.TLSCA != "" {
			caCert, err := os.ReadFile(cfg.TLSCA)
			if err != nil {
				return nil, fmt.Errorf("failed to load snake tls ca: %w", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			opts.TLSConfig.ClientCAs = caCertPool
			if cfg.TLSVerify {
				opts.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
			}
		}

		// DO NOT set opts.TLS = true
		// DO NOT set opts.TLSCert / opts.TLSKey (as they force TLS)
		// AllowNonTLS defaults to false: configuring TLS makes it required,
		// not merely offered. A caller must opt in explicitly (snake.allow_non_tls)
		// to keep accepting plaintext clients alongside TLS ones.
		opts.AllowNonTLS = cfg.AllowNonTLS
		opts.TLSVerify = cfg.TLSVerify
	}

	// JetStream Configuration
	// opts.JetStreamDomain = cfg.ClusterName // Disabled for restoration (use default)

	// Pre-flight check: is the port available?
	if cfg.Port > 0 {
		addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.Port))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("snake port %d is already in use: %w", cfg.Port, err)
		}
		_ = l.Close()
	}

	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}

	// Start NATS
	slog.Info("Starting NATS server...", "port", opts.Port)
	go ns.Start()

	// Wait for readiness
	slog.Info("Waiting for NATS readiness...")
	ready := make(chan bool, 1)
	go func() {
		ready <- ns.ReadyForConnections(5 * time.Second)
	}()

	select {
	case <-ctx.Done():
		slog.Warn("Snake startup context canceled")
		ns.Shutdown()
		return nil, ctx.Err()
	case isReady := <-ready:
		if !isReady {
			ns.Shutdown()
			return nil, fmt.Errorf("nats server failed to start on port %d (timeout): every other startup check already passed, so this usually means the store in %s could not be opened or recovered in time; if it keeps happening, move the directory away to start an empty store", cfg.Port, cfg.StoreDir)
		}
	}
	slog.Info("NATS server is ready")

	s := &Server{
		ns:             ns,
		domain:         cfg.ClusterName,
		streamMaxAge:   cfg.StreamMaxAge,
		streamMaxBytes: cfg.StreamMaxBytes,
		kvMaxBytes:     cfg.KVMaxBytes,
		kvMaxValueSize: cfg.KVMaxValueSize,
		kvTTL:          cfg.KVTTL,
	}

	// 4. Provision Streams
	if cfg.StreamName != "" {
		slog.Info("Provisioning streams...", "name", cfg.StreamName)
		if err := s.ProvisionStream(ctx, cfg.StreamName, cfg.StreamSubjects); err != nil {
			s.Shutdown()
			if _, errMarker := os.Stat(markerPath); cfg.StoreKey != "" && errMarker == nil {
				return nil, fmt.Errorf("snake: the store in %s did not open with the current key, and it was encrypted before: the cluster key or the store key file has changed since. Put the previous key back, or name it in the store's old key file to rotate to the new one, or move the directory away to start an empty store: %w", cfg.StoreDir, err)
			}
			// Not a key problem: whatever is wrong with the store in
			// cfg.StoreDir (a corrupted or otherwise unreadable stream),
			// there is no automatic recovery here on purpose - discarding a
			// store that might still hold undelivered guaranteed messages
			// without an operator's say-so is its own kind of data loss.
			// Moving the directory away starts a genuinely empty store.
			return nil, fmt.Errorf("snake: failed to provision stream %q in %s: %w (if the store cannot be repaired, move the directory away to start an empty one)", cfg.StreamName, cfg.StoreDir, err)
		}
		slog.Info("Streams provisioned")
	}

	if cfg.StoreKey != "" {
		if err := os.WriteFile(markerPath, []byte("encrypted\n"), 0o600); err != nil {
			s.Shutdown()
			return nil, fmt.Errorf("snake: record that the store is encrypted: %w", err)
		}
	}

	return s, nil
}

// ProvisionStream checks if a stream exists and creates it if not.
func (s *Server) ProvisionStream(ctx context.Context, name string, subjects []string) error {

	// Connect to self with retry resilience
	var nc *nats.Conn
	var err error
	slog.Info("Snake connecting in-process for provisioning")
	for i := 1; i <= 3; i++ {
		nc, err = s.InProcessConn()
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

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
		cfg.MaxAge = s.streamMaxAge
		// Applied unconditionally, like MaxAge above: zero is a real, documented
		// setting ("no limit"), not "leave whatever the stream already has". An
		// operator who lowers, or clears, a limit on an existing stream expects
		// the next start to apply it, not to silently keep the old one forever.
		cfg.MaxBytes = s.streamMaxBytes
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
		MaxAge:    s.streamMaxAge,
		MaxBytes:  s.streamMaxBytes,
	})

	if err != nil {
		return fmt.Errorf("snake: failed to create stream %s: %w", name, err)
	}

	return nil
}

// ProvisionKV checks if a KeyValue bucket exists and creates it if not.
func (s *Server) ProvisionKV(ctx context.Context, bucket string) error {
	var nc *nats.Conn
	var err error
	slog.Info("Snake connecting in-process for KV provisioning")
	for i := 1; i <= 3; i++ {
		nc, err = s.InProcessConn()
		if err == nil {
			break
		}
		if i < 3 {
			slog.Info("Snake KV provisioning connect failed, retrying...", "attempt", i, "error", err)
			time.Sleep(250 * time.Millisecond)
		}
	}

	if err != nil {
		return fmt.Errorf("snake: failed to connect for KV provisioning after 3 attempts: %w", err)
	}
	defer nc.Close()

	var js jetstream.JetStream
	var jsErr error
	js, jsErr = jetstream.New(nc)
	if jsErr != nil {
		return fmt.Errorf("snake: failed to initialize jetstream: %w", jsErr)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Create or update: applied unconditionally, like the stream limits
	// above, so an operator who tightens a limit on an already-provisioned
	// bucket expects the next start to apply it, not to silently keep
	// whatever the bucket already has forever.
	slog.Info("Provisioning JetStream KV bucket", "bucket", bucket, "max_bytes", s.kvMaxBytes, "max_value_size", s.kvMaxValueSize, "ttl", s.kvTTL)
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:       bucket,
		Storage:      jetstream.FileStorage,
		Replicas:     1,
		MaxBytes:     s.kvMaxBytes,
		MaxValueSize: s.kvMaxValueSize,
		TTL:          s.kvTTL,
	})

	if err != nil {
		return fmt.Errorf("snake: failed to create KV bucket %s: %w", bucket, err)
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

// InProcessConn returns a NATS connection bound to the embedded server
// through nats-server's own net.Pipe-based transport (*server.Server
// already implements nats.InProcessConnProvider), never through its TCP
// listener. That keeps this connection outside the TLS/AllowNonTLS policy
// entirely: it is not a network client the security posture is about, and,
// unlike a URL rewritten from "tls://" to "nats://" and dialed for real,
// it cannot be refused or forced into a certificate check by that policy.
func (s *Server) InProcessConn(natsOpts ...nats.Option) (*nats.Conn, error) {
	opts := append([]nats.Option{nats.InProcessServer(s.ns)}, natsOpts...)
	return nats.Connect(nats.DefaultURL, opts...) // URL is unused once InProcessServer is set
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
