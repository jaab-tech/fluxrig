// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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

	// replicas is how many copies of every stream and bucket this server
	// provisions. Always at least 1, and 1 on the embedded server.
	replicas int
	// externalURL is set when there is no embedded server: the Server is then a
	// client of a NATS cluster that the operator runs. clientOpts is how it connects.
	externalURL string
	clientOpts  []nats.Option

	// Limits applied to every stream this server provisions. Zero means none.
	streamMaxAge   time.Duration
	streamMaxBytes int64

	// Limits applied to every KV bucket this server provisions. Zero means
	// none for kvTTL (keys never expire); kvMaxBytes/kvMaxValueSize follow
	// NATS's own convention where zero is also "unlimited" for that field.
	kvMaxBytes     int64
	kvMaxValueSize int32
	kvTTL          time.Duration
	// formTimeout bounds the retries of a provisioning on an external NATS, and
	// provisionTimeout one attempt (see Config.ProvisionTimeout).
	formTimeout      time.Duration
	provisionTimeout time.Duration
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

// defaultFormTimeout is generous on purpose: an operator restarting a NATS
// cluster brings its servers up one after another.
const defaultFormTimeout = 2 * time.Minute

// defaultProvisionTimeout bounds one stream provisioning attempt when the
// caller sets none.
const defaultProvisionTimeout = 10 * time.Second

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

	// ProvisionTimeout bounds one attempt to create or update a stream. On an
	// external NATS a failed attempt is retried until FormTimeout, so a request
	// lost while the cluster forms costs one attempt, not the whole start. Zero
	// takes defaultProvisionTimeout.
	ProvisionTimeout time.Duration
	// FormTimeout is how long a Server on an external NATS waits for it to
	// answer, and retries a provisioning. Zero takes defaultFormTimeout.
	FormTimeout time.Duration

	// StreamMaxAge and StreamMaxBytes limit every stream the server provisions.
	// Zero means no limit.
	StreamMaxAge   time.Duration
	StreamMaxBytes int64

	// KVMaxBytes, KVMaxValueSize and KVTTL limit every KV bucket the server
	// provisions, the same way StreamMaxAge/StreamMaxBytes limit streams.
	KVMaxBytes     int64
	KVMaxValueSize int32
	KVTTL          time.Duration

	// Replicas is how many copies of every stream and bucket this server
	// provisions. Zero or one is a single copy, the only one the embedded
	// server takes. More than one needs an external NATS cluster with at
	// least that many servers.
	Replicas int
}

// MinExternalNATS is the oldest NATS server a Mixer accepts. It is the version
// that the HA gates ran on. Per-message TTL, which the buckets need, is older, so
// this can go down once an older version has passed them.
const MinExternalNATS = "2.15.0"

// NewExternal returns a Server with no embedded NATS: it provisions streams and
// buckets on the NATS at url, a comma-separated list. The operator runs that
// NATS, so there is nothing to start and nothing to stop. It waits for that NATS
// until the form timeout, then refuses one it cannot run on, with the reason.
func NewExternal(ctx context.Context, cfg Config, url string, clientOpts ...nats.Option) (*Server, error) {
	replicas := cfg.Replicas
	if replicas < 1 {
		replicas = 1
	}
	formTimeout := cfg.FormTimeout
	if formTimeout <= 0 {
		formTimeout = defaultFormTimeout
	}
	provisionTimeout := cfg.ProvisionTimeout
	if provisionTimeout <= 0 {
		provisionTimeout = defaultProvisionTimeout
	}
	s := &Server{
		domain:           cfg.ClusterName,
		replicas:         replicas,
		externalURL:      url,
		clientOpts:       clientOpts,
		streamMaxAge:     cfg.StreamMaxAge,
		streamMaxBytes:   cfg.StreamMaxBytes,
		kvMaxBytes:       cfg.KVMaxBytes,
		kvMaxValueSize:   cfg.KVMaxValueSize,
		kvTTL:            cfg.KVTTL,
		formTimeout:      formTimeout,
		provisionTimeout: provisionTimeout,
	}
	if err := s.checkExternal(ctx); err != nil {
		return nil, err
	}
	if cfg.StreamName != "" {
		if err := s.provisionWithRetry(ctx, cfg.StreamName, cfg.StreamSubjects, formTimeout); err != nil {
			return nil, fmt.Errorf("snake: provision the stream on the external NATS: %w", err)
		}
	}
	return s, nil
}

// checkExternal connects to the external NATS and refuses one that this Mixer
// cannot run on. A wrong version, JetStream off or no cluster are final:
// waiting does not change them, and the operator needs the reason at once.
// Anything else is retried until the form timeout: a cluster that is still
// starting refuses the connection, or lets a request time out while it elects
// its metadata leader.
func (s *Server) checkExternal(ctx context.Context) error {
	deadline := time.Now().Add(s.formTimeout)
	for {
		nc, err := s.InProcessConn()
		if err == nil {
			err = s.checkExternalConn(ctx, nc)
			nc.Close()
			var final finalError
			if err == nil || errors.As(err, &final) {
				return err
			}
		} else {
			err = fmt.Errorf("snake: the external NATS at %s does not answer: %w", s.externalURL, err)
		}
		if time.Now().After(deadline) {
			return err
		}
		slog.Info("Waiting for the external NATS", "url", s.externalURL, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// finalError is a refusal of the external NATS that waiting does not change.
type finalError struct{ error }

func (e finalError) Unwrap() error { return e.error }

func (s *Server) checkExternalConn(ctx context.Context, nc *nats.Conn) error {
	version := nc.ConnectedServerVersion()
	ok, err := versionAtLeast(version, MinExternalNATS)
	if err != nil {
		return finalError{fmt.Errorf("snake: read the NATS version: %w", err)}
	}
	if !ok {
		return finalError{fmt.Errorf("snake: the external NATS runs %s, and fluxrig needs %s or later", version, MinExternalNATS)}
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("snake: open jetstream: %w", err)
	}
	actx, cancel := context.WithTimeout(ctx, s.provisionTimeout)
	defer cancel()
	if _, err = js.AccountInfo(actx); err != nil {
		err = fmt.Errorf("snake: the external NATS has no JetStream for this account: %w", err)
		if errors.Is(err, jetstream.ErrJetStreamNotEnabled) || errors.Is(err, jetstream.ErrJetStreamNotEnabledForAccount) {
			return finalError{err}
		}
		return err
	}
	if s.replicas > 1 && nc.ConnectedClusterName() == "" {
		return finalError{fmt.Errorf("snake: snake.replicas is %d, and the external NATS is not a cluster", s.replicas)}
	}
	if s.replicas < 3 {
		slog.Warn("snake.replicas is below 3: the loss of one NATS server stops the streams", "replicas", s.replicas)
	}
	slog.Info("External NATS accepted", "version", version, "cluster", nc.ConnectedClusterName(), "replicas", s.replicas)
	return nil
}

// versionAtLeast reports whether version is at least minimum, by major, minor and
// patch. A pre-release suffix is ignored.
func versionAtLeast(version, minimum string) (bool, error) {
	have, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	want, err := parseVersion(minimum)
	if err != nil {
		return false, err
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i], nil
		}
	}
	return true, nil
}

func parseVersion(v string) ([3]int, error) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("version %q is not major.minor.patch", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("version %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}

// External reports whether this Server has no embedded NATS.
func (s *Server) External() bool { return s.externalURL != "" }

// NewServer creates and starts an embedded NATS server with JetStream enabled.
//
// The embedded server runs alone: it holds one copy of each stream, and HA
// takes an external NATS cluster (NewExternal).
func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.Replicas > 1 {
		return nil, fmt.Errorf("snake: %d replicas need an external NATS cluster; the embedded server runs alone", cfg.Replicas)
	}
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
	ns.SetLogger(newNATSLogger(), opts.Debug, opts.Trace)

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
		ns:               ns,
		domain:           cfg.ClusterName,
		replicas:         1,
		streamMaxAge:     cfg.StreamMaxAge,
		streamMaxBytes:   cfg.StreamMaxBytes,
		kvMaxBytes:       cfg.KVMaxBytes,
		kvMaxValueSize:   cfg.KVMaxValueSize,
		kvTTL:            cfg.KVTTL,
		provisionTimeout: cfg.ProvisionTimeout,
	}

	if s.provisionTimeout <= 0 {
		s.provisionTimeout = defaultProvisionTimeout
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
			// Diagnose and repair it with the Mixer stopped:
			// fluxrig-mixer -c <config> --repair
			// Moving the directory away starts a genuinely empty store.
			return nil, fmt.Errorf("snake: failed to provision stream %q in %s: %w (diagnose with: fluxrig-mixer -c <config> --repair; if the store cannot be repaired, move the directory away to start an empty one)", cfg.StreamName, cfg.StoreDir, err)
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

// provisionWithRetry creates or updates a stream, retrying until the deadline.
// Placing a stream's replicas needs one server per replica, and the last one
// may still be starting, so a first failure is expected rather than fatal.
func (s *Server) provisionWithRetry(ctx context.Context, name string, subjects []string, timeout time.Duration) error {
	return s.retryProvision(ctx, name, timeout, func() error { return s.ProvisionStream(ctx, name, subjects) })
}

// retryProvision runs provision until it succeeds, retrying on an external
// NATS until timeout: a server that is still joining, or a request lost while
// the cluster forms, fails one attempt and not the start. The embedded server
// has nothing to wait for, so a failure there is final.
func (s *Server) retryProvision(ctx context.Context, name string, timeout time.Duration, provision func() error) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		err := provision()
		if err == nil || !s.External() || time.Now().After(deadline) {
			return err
		}
		slog.Info("Stream provisioning is waiting for the external NATS", "stream", name, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// ProvisionStream checks if a stream exists and creates it if not.
func (s *Server) ProvisionStream(ctx context.Context, name string, subjects []string) error {
	return s.provisionStream(ctx, name, subjects, s.streamMaxAge, s.streamMaxBytes)
}

// ProvisionStreamWithAge is ProvisionStream with an explicit max age, for a
// stream whose retention is its own rather than the message stream's. The dead
// letter stream uses it: a poison message should age out on its own schedule,
// not the business stream's. An empty or unparseable age falls back to the
// server's own streamMaxAge.
func (s *Server) ProvisionStreamWithAge(ctx context.Context, name string, subjects []string, maxAge string) error {
	age := s.streamMaxAge
	if maxAge != "" {
		if parsed, err := time.ParseDuration(maxAge); err == nil {
			age = parsed
		}
	}
	return s.retryProvision(ctx, name, s.formTimeout, func() error {
		return s.provisionStream(ctx, name, subjects, age, s.streamMaxBytes)
	})
}

// ProvisionStreamWithLimits is ProvisionStream with a retention of its own, in
// age and in bytes. The stream of durable events uses it: its content is short
// lived, and it must not share the limits of the message stream.
func (s *Server) ProvisionStreamWithLimits(ctx context.Context, name string, subjects []string, maxAge time.Duration, maxBytes int64) error {
	return s.retryProvision(ctx, name, s.formTimeout, func() error {
		return s.provisionStream(ctx, name, subjects, maxAge, maxBytes)
	})
}

func (s *Server) provisionStream(ctx context.Context, name string, subjects []string, maxAge time.Duration, maxBytes int64) error {

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

	attemptTimeout := s.provisionTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = defaultProvisionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
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
		cfg.MaxAge = maxAge
		// Applied unconditionally, like MaxAge above: zero is a real, documented
		// setting ("no limit"), not "leave whatever the stream already has". An
		// operator who lowers, or clears, a limit on an existing stream expects
		// the next start to apply it, not to silently keep the old one forever.
		cfg.MaxBytes = maxBytes
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
		Replicas:  s.replicas,
		MaxAge:    maxAge,
		MaxBytes:  maxBytes,
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

	// Create or update: applied unconditionally, like the stream limits
	// above, so an operator who tightens a limit on an already-provisioned
	// bucket expects the next start to apply it, not to silently keep
	// whatever the bucket already has forever.
	//
	// Placing a bucket's replicas needs one server per replica, and on a fresh
	// cluster the last one may still be starting, so a single short attempt
	// fails where a retry succeeds. The window matches stream provisioning.
	const attemptTimeout = 5 * time.Second
	deadline := time.Now().Add(s.formTimeout)
	var createErr error
	for attempt := 1; ; attempt++ {
		slog.Info("Provisioning JetStream KV bucket", "bucket", bucket, "max_bytes", s.kvMaxBytes, "max_value_size", s.kvMaxValueSize, "ttl", s.kvTTL, "attempt", attempt)
		actx, cancel := context.WithTimeout(ctx, attemptTimeout)
		_, createErr = js.CreateOrUpdateKeyValue(actx, jetstream.KeyValueConfig{
			Bucket:       bucket,
			Storage:      jetstream.FileStorage,
			Replicas:     s.replicas,
			MaxBytes:     s.kvMaxBytes,
			MaxValueSize: s.kvMaxValueSize,
			TTL:          s.kvTTL,
		})
		cancel()
		if createErr == nil {
			return nil
		}
		if !s.External() || time.Now().After(deadline) {
			return fmt.Errorf("snake: failed to create KV bucket %s: %w", bucket, createErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("snake: failed to create KV bucket %s: %w", bucket, ctx.Err())
		case <-time.After(time.Second):
		}
	}
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
	if s.External() {
		return s.externalURL
	}
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
	if s.External() {
		return nats.Connect(s.externalURL, append(append([]nats.Option{}, s.clientOpts...), natsOpts...)...)
	}
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
	if s.External() {
		return nil, nil
	}
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
	if s.External() {
		return map[string]any{"external": true}
	}
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
