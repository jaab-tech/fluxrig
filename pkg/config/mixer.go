// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"math"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// MixerConfig defines the startup configuration for the Mixer.
type MixerConfig struct {
	Base          BaseConfig          `koanf:"base"`
	Logging       LoggingConfig       `koanf:"logging"`
	Store         StoreConfig         `koanf:"store"`
	Mixer         MixerSettings       `koanf:"mixer"`
	API           ApiConfig           `koanf:"api"`
	Snake         SnakeConfig         `koanf:"snake"`
	Observability ObservabilityConfig `koanf:"observability"`
	Enrollment    EnrollmentConfig    `koanf:"enrollment"`
	Ingest        IngestConfig        `koanf:"ingest"`
	Telemetry     TelemetryConfig     `koanf:"telemetry"`
	Wasm          WasmConfig          `koanf:"wasm"`
	Registry      RegistryConfig      `koanf:"registry"`
}

// RegistryConfig sets where the Mixer keeps its registry: Rack records, the
// name index, scenario files and the active-scenario pointer, in one
// JetStream key-value bucket, and presence in a second one whose max age
// expires it.
type RegistryConfig struct {
	// Bucket names the registry bucket.
	Bucket string `koanf:"bucket" example:"fluxrig_registry"`
	// LivenessBucket names the presence bucket.
	LivenessBucket string `koanf:"liveness_bucket" example:"fluxrig_liveness"`
	// Replicas is the replica count of both buckets. A single Mixer holds one
	// replica, so a value above 1 needs as many JetStream servers.
	Replicas int `koanf:"replicas" example:"1"`
	// MaxValueSize is the largest record in bytes, headers included. In
	// practice it bounds a scenario file.
	MaxValueSize int `koanf:"max_value_size" example:"1048576"`
	// LivenessTTL is how long after its last heartbeat a Rack still counts
	// as online. Three heartbeat intervals by default.
	LivenessTTL string `koanf:"liveness_ttl" example:"90s"`
	// WriteRetries is how many times a registry write that met a concurrent
	// one is read again and retried before it fails.
	WriteRetries int `koanf:"write_retries" example:"3"`
	// SweepInterval is how often the Mixer repairs what a crash between two
	// registry writes left behind. It also sweeps once at startup.
	SweepInterval string `koanf:"sweep_interval" example:"10m"`
	// OrphanMinAge is how old a registry record must be before the sweep
	// treats it as left behind rather than in progress.
	OrphanMinAge string `koanf:"orphan_min_age" example:"5m"`
	// LeaseEnabled turns on the write lease: one Mixer holds it and does
	// every control-plane write, so several Mixers never write the registry
	// at once. Off by default, which keeps a single Mixer exactly as it was.
	LeaseEnabled bool `koanf:"lease_enabled" example:"false"`
	// LeaseBucket names the bucket that holds the lease. It must differ from
	// the registry and liveness buckets.
	LeaseBucket string `koanf:"lease_bucket" example:"fluxrig_lease"`
	// LeaseTTL is the lease duration: the bucket's max age, and how long a
	// holder may go without a successful renew before it stops writing. Sized
	// against the slowest election at cross-site latency.
	LeaseTTL string `koanf:"lease_ttl" example:"30s"`
	// LeaseRenew is how often the holder renews. It must be well under
	// LeaseTTL so an ordinary slow write does not lapse the lease.
	LeaseRenew string `koanf:"lease_renew" example:"10s"`
	// SpecBucket names the bucket that holds the shared spec store. It is
	// used with the lease on: every Mixer of the cluster reads and imports
	// specs there. A single Mixer keeps its local store.
	SpecBucket string `koanf:"spec_bucket" example:"fluxrig_specs"`
	// SpecMaxSize is the largest spec in bytes the shared store accepts. It
	// is the spec bucket's max value size, so it must not exceed the Snake's
	// max_payload (1 MiB by default).
	SpecMaxSize int `koanf:"spec_max_size" example:"1048576"`
}

// WasmConfig defines settings for the WebAssembly Supply Chain Security Catalog.
type WasmConfig struct {
	CatalogDir     string `koanf:"catalog_dir" example:"./data/wasm"`
	TrustedKeysDir string `koanf:"trusted_keys_dir" example:"./data/wasm/keys"`
}

// IngestConfig controls telemetry ingestion buffering and flushing.
type IngestConfig struct {
	// FlushInterval determines how often to flush data to Parquet.
	FlushInterval string `koanf:"flush_interval" example:"5s"`
	// BufferSize is the internal channel buffer size (deprecated).
	BufferSize int `koanf:"buffer_size" example:"1024"`
}

// EnrollmentConfig controls how new Racks are enrolled.
type EnrollmentConfig struct {
	// PushDelay is the time to wait before pushing state to a newly enrolled Rack.
	PushDelay string `koanf:"push_delay" example:"1s"`
	// AutoAdopt, if true, will automatically mark newly enrolled Racks as 'active'.
	// If false (default), new Racks start as 'pending'.
	AutoAdopt bool `koanf:"auto_adopt" example:"false"`
	// BootstrapSecret is the shared secret used for zero-config enrollment and identity adoption.
	// Defaults to 'fluxrig' if not specified. Never serialized: handleConfig
	// encodes this struct with no json tags elsewhere in it, which means the
	// default (no-tag) behavior would otherwise print it under its Go field
	// name in the API response.
	BootstrapSecret string `koanf:"bootstrap_secret" json:"-" cbor:"-" example:"fluxrig"`
}

// ObservabilityConfig controls the global observability tier.
type ObservabilityConfig struct {
	// Enabled master switch for observability features.
	Enabled bool `koanf:"enabled" example:"true"`
	// Tier selects the backend (embedded or standard).
	Tier string `koanf:"tier" example:"embedded"`
	// Embedded configures the local embedded stack.
	Embedded EmbeddedConfig `koanf:"embedded"`
	// Storage is where the Mixer keeps telemetry.
	Storage StorageConfig `koanf:"storage"`
}

// Telemetry storage types.
const (
	StorageLocal = "local"
	StorageS3    = "s3"
	StorageNATS  = "nats"
	StorageOTLP  = "otlp"
)

// StorageConfig is where the Mixer keeps the telemetry of its Racks.
//
// local, the default, keeps it on this Mixer's disk. With several Mixers
// (snake.external) each one then keeps a copy of its own, which misses what
// arrived while it was down. s3, nats and otlp are for several Mixers: the
// Mixer that holds the write lease is the only one that reads the telemetry
// stream, and it confirms each batch only once the store has it, so no record
// is kept twice or lost. s3 and nats keep Parquet files every Mixer queries;
// otlp sends the telemetry to the operator's collector, and the Mixers keep
// none.
type StorageConfig struct {
	// Type is local, s3, nats or otlp.
	Type string `koanf:"type" example:"local"`
	// CommitTimeout bounds one upload or export of a batch. A batch that does
	// not make it is tried again, and stays unconfirmed until it does.
	CommitTimeout string `koanf:"commit_timeout" example:"30s"`
	// BatchInterval is how often the lease holder commits a batch: it writes
	// one file of each kind to s3 or nats, or sends one export to otlp. The
	// other Mixers see the telemetry up to the last batch. Each Mixer keeps
	// the files it writes or fetches for a query under store.dir/telemetry,
	// under the same retention as local files.
	BatchInterval string `koanf:"batch_interval" example:"15s"`
	// S3, NATS and OTLP configure their type.
	S3   S3StorageConfig   `koanf:"s3"`
	NATS NATSStorageConfig `koanf:"nats"`
	OTLP OTLPStorageConfig `koanf:"otlp"`
}

// S3StorageConfig is a bucket of an S3-compatible object store. The Mixer
// requests SSE-S3 for every uploaded telemetry object.
type S3StorageConfig struct {
	// Endpoint is the host and port of the service, without a scheme.
	Endpoint string `koanf:"endpoint" example:"s3.amazonaws.com"`
	Bucket   string `koanf:"bucket" example:"fluxrig-telemetry"`
	// Prefix is put before every object name.
	Prefix string `koanf:"prefix" example:"telemetry"`
	Region string `koanf:"region" example:"us-east-1"`
	// AccessKeyFile and SecretKeyFile hold the credentials. Empty reads
	// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
	AccessKeyFile string `koanf:"access_key_file"`
	SecretKeyFile string `koanf:"secret_key_file"`
	// Plaintext talks HTTP instead of HTTPS. Development only.
	Plaintext bool `koanf:"plaintext" example:"false"`
	// CAFile verifies the service's certificate, when a public CA does not.
	CAFile string `koanf:"ca_file"`
}

// NATSStorageConfig is an object store bucket on the NATS of the Mixers. It
// takes snake.replicas copies.
type NATSStorageConfig struct {
	Bucket string `koanf:"bucket" example:"fluxrig_telemetry"`
}

// OTLPStorageConfig is an OpenTelemetry collector that takes OTLP over HTTP.
type OTLPStorageConfig struct {
	// Endpoint is the collector's base URL; the paths /v1/logs, /v1/metrics
	// and /v1/traces are added to it.
	Endpoint string `koanf:"endpoint" example:"https://collector.example:4318"`
	// CAFile verifies the collector's certificate, when a public CA does not.
	CAFile string `koanf:"ca_file"`
	// HeadersFile holds extra request headers, one "Name: value" per line,
	// such as an authorization token.
	HeadersFile string `koanf:"headers_file"`
}

// EmbeddedConfig settings for the embedded observability stack.
type EmbeddedConfig struct {
	// FlushInterval for the embedded stack.
	FlushInterval string `koanf:"flush_interval" example:"5s"`
	// RetentionDays for local parquet files. Wins over
	// Rotation.MaxAgeDays when set to a positive value.
	RetentionDays int `koanf:"retention_days" example:"30"`
	// Rotation controls the retention sweep over local parquet files.
	Rotation RotationConfig `koanf:"rotation"`
}

// RotationConfig controls the retention sweep over local parquet files.
type RotationConfig struct {
	// CheckInterval is how often the retention sweep runs. The sweep
	// also runs once at Mixer startup.
	CheckInterval string `koanf:"check_interval" example:"1h"`
	// MaxAgeDays deletes parquet files older than this many days when
	// RetentionDays is not set to a positive value.
	MaxAgeDays int `koanf:"max_age_days" example:"30"`
}

// MixerSettings defines the identity of this Mixer instance.
type MixerSettings struct {
	// StartupScenario reference to the scenario to load on startup.
	StartupScenario string `koanf:"startup_scenario"`
	// Message Limits
	MaxHops        int `koanf:"max_hops"`
	MaxPayloadSize int `koanf:"max_payload_size"`
	// ScenarioWaitTimeout is the time to wait for a rack to register when activating a scenario.
	ScenarioWaitTimeout string `koanf:"scenario_wait_timeout" example:"15s"`
	// ForwardTimeout bounds a control write forwarded to the lease holder. It
	// must cover a cross-site round trip plus the holder's own write.
	ForwardTimeout string `koanf:"forward_timeout" example:"5s"`
	// FollowInterval is how often a Mixer that does not hold the write lease
	// checks the active-scenario pointer and reloads it when it changed.
	FollowInterval string `koanf:"follow_interval" example:"5s"`
	// QueueGroup, when set, makes a Mixer's subscriptions a shared durable
	// queue group: exactly one member handles each message. It is turned on
	// automatically with the write lease, so several Mixers do not each
	// process the same Hello. Empty keeps every Mixer subscribing alone.
	QueueGroup string `koanf:"queue_group" example:"fluxrig-mixers"`
}

// ApiConfig settings for the Control Plane REST API.
type ApiConfig struct {
	// Port to listen on.
	Port int `koanf:"port" example:"8090"`
	// ReadHeaderTimeout for the HTTP server.
	ReadHeaderTimeout string `koanf:"read_header_timeout" example:"3s"`
	// TLSCertFile path to server certificate.
	TLSCertFile string `koanf:"tls_cert_file" example:"server.crt"`
	// TLSKeyFile path to server key.
	TLSKeyFile string `koanf:"tls_key_file" example:"server.key"`
	// ControlConfirmTimeout bounds how long a control-plane command (the
	// simulator start/stop/rate/reset endpoints) waits for a gear to
	// acknowledge receiving it before the request answers that nobody is
	// listening, rather than the success a bare, unconfirmed publish would
	// have reported.
	ControlConfirmTimeout string `koanf:"control_confirm_timeout" example:"2s"`
	// ApprovalPushTimeout bounds the push of the active scenario to a Rack
	// that an operator approves. The push does not end when the request
	// does: a client that disconnects must not leave the Rack half served.
	ApprovalPushTimeout string `koanf:"approval_push_timeout" example:"10s"`
	// AuthToken gates every management-API route except health. A request
	// must send it as "Authorization: Bearer <token>". Never serialized.
	AuthToken string `koanf:"auth_token" json:"-" cbor:"-" example:""`
	// AuthDisabledDangerously must be set explicitly to start the Mixer with
	// no AuthToken configured. Without either, Start refuses to run: silently
	// serving the management API unauthenticated is the exact failure mode
	// this field exists to require an explicit, named opt-out from.
	AuthDisabledDangerously bool `koanf:"auth_disabled_dangerously" example:"false"`
}

// LoadMixer reads configuration from a TOML file and Environment Variables.
// Priority: Env > File > Defaults. If path is empty, it returns the default configuration.
func LoadMixer(path string) (*MixerConfig, error) {
	k := koanf.New(".")

	// 1. Defaults
	_ = k.Set("base.name", "fluxrig-mixer")
	_ = k.Set("base.state_dir", "./data")

	_ = k.Set("logging.level", "info")
	_ = k.Set("logging.filename", "logs/mixer.log")
	_ = k.Set("logging.max_size_mb", 100)
	_ = k.Set("logging.max_backups", 7)
	_ = k.Set("logging.compress", true)

	// Defaults: Store
	_ = k.Set("store.dir", "./data")
	_ = k.Set("store.wal_max_size_mb", 500)
	_ = k.Set("store.database_file", "flux.duckdb")
	_ = k.Set("store.cluster_key_file", "cluster.key")

	_ = k.Set("mixer.max_hops", 64)
	_ = k.Set("mixer.max_payload_size", 2*1024*1024) // 2MB
	_ = k.Set("mixer.scenario_wait_timeout", "15s")
	_ = k.Set("mixer.forward_timeout", "5s")
	_ = k.Set("mixer.queue_group", "fluxrig-mixers")
	_ = k.Set("mixer.follow_interval", "5s")
	_ = k.Set("api.port", 8090)
	_ = k.Set("api.read_header_timeout", "3s")
	_ = k.Set("api.control_confirm_timeout", "2s")
	_ = k.Set("api.approval_push_timeout", "10s")
	_ = k.Set("api.auth_token", "")
	_ = k.Set("api.auth_disabled_dangerously", false)

	// Defaults: Snake
	_ = k.Set("snake.url", "nats://localhost:4222")
	_ = k.Set("snake.port", 4222)
	_ = k.Set("snake.domain", "flux")
	_ = k.Set("snake.cluster_name", "flux")
	_ = k.Set("snake.stream_name", "flux-msg")
	_ = k.Set("snake.replicas", 1)
	_ = k.Set("snake.form_timeout", "2m")
	_ = k.Set("snake.operation_timeout", "5s")
	_ = k.Set("snake.ping_interval", DefaultPingInterval)
	_ = k.Set("snake.max_pings_out", DefaultMaxPingsOut)
	_ = k.Set("snake.ack_wait", DefaultAckWait)
	_ = k.Set("snake.inactive_threshold", "30s")
	_ = k.Set("snake.store_encryption", true)
	_ = k.Set("snake.store_cipher", "chacha")
	_ = k.Set("snake.stream_max_age", "24h")
	_ = k.Set("snake.stream_max_bytes", 1073741824)
	_ = k.Set("snake.allow_non_tls", false)
	_ = k.Set("snake.tls_verify", false)
	// kv_max_bytes matches stream_max_bytes; kv_max_value_size matches the
	// embedded NATS server's own default max payload (it does not raise it),
	// so the value is a real ceiling rather than an unenforceable number.
	_ = k.Set("snake.kv_max_bytes", 1073741824)
	_ = k.Set("snake.kv_max_value_size", 1048576)
	_ = k.Set("snake.kv_ttl", "0s")
	_ = k.Set("snake.dlq_enabled", true)
	_ = k.Set("snake.dlq_subject", "flux.dlq")
	_ = k.Set("snake.dlq_max_age", "720h")
	_ = k.Set("snake.dlq_publish_timeout", "5s")
	_ = k.Set("snake.events_max_age", "1h")
	_ = k.Set("snake.events_max_bytes", 268435456)

	// Defaults: Telemetry (Self-Monitoring)
	_ = k.Set("telemetry.service_name", "flux.mixer")
	_ = k.Set("telemetry.batch_interval", "5s")
	_ = k.Set("telemetry.base_subject", "flux.telemetry")
	_ = k.Set("telemetry.max_batch_size", 512)
	_ = k.Set("telemetry.stream_name", "flux-telemetry")

	_ = k.Set("ingest.flush_interval", "5s")
	_ = k.Set("ingest.buffer_size", 1024)

	_ = k.Set("observability.tier", "embedded")
	_ = k.Set("observability.embedded.flush_interval", "5s")
	_ = k.Set("observability.storage.type", StorageLocal)
	_ = k.Set("observability.storage.commit_timeout", "30s")
	_ = k.Set("observability.storage.batch_interval", "15s")
	_ = k.Set("observability.storage.s3.prefix", "telemetry")
	_ = k.Set("observability.storage.nats.bucket", "fluxrig_telemetry")
	_ = k.Set("observability.embedded.retention_days", 30)
	_ = k.Set("observability.embedded.rotation.check_interval", "1h")
	_ = k.Set("observability.embedded.rotation.max_age_days", 30)
	_ = k.Set("enrollment.push_delay", "1s")
	_ = k.Set("enrollment.auto_adopt", false)
	_ = k.Set("enrollment.bootstrap_secret", "fluxrig")

	_ = k.Set("registry.bucket", DefaultRegistryBucket)
	_ = k.Set("registry.liveness_bucket", DefaultRegistryLivenessBucket)
	_ = k.Set("registry.replicas", DefaultRegistryReplicas)
	_ = k.Set("registry.max_value_size", DefaultRegistryMaxValueSize)
	_ = k.Set("registry.liveness_ttl", DefaultRegistryLivenessTTL)
	_ = k.Set("registry.write_retries", DefaultRegistryWriteRetries)
	_ = k.Set("registry.sweep_interval", DefaultRegistrySweepInterval)
	_ = k.Set("registry.orphan_min_age", DefaultRegistryOrphanMinAge)
	_ = k.Set("registry.lease_enabled", false)
	_ = k.Set("registry.lease_bucket", DefaultRegistryLeaseBucket)
	_ = k.Set("registry.spec_bucket", DefaultRegistrySpecBucket)
	_ = k.Set("registry.spec_max_size", DefaultRegistrySpecMaxSize)
	_ = k.Set("registry.lease_ttl", DefaultRegistryLeaseTTL)
	_ = k.Set("registry.lease_renew", DefaultRegistryLeaseRenew)

	_ = k.Set("wasm.catalog_dir", "./data/wasm")
	_ = k.Set("wasm.trusted_keys_dir", "./data/wasm/keys")

	// 2. File (if provided)
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return nil, err
		}
		// The file's own keys are validated separately from the merged set,
		// because the FLUXRIG_ environment namespace also carries process
		// control variables that are not configuration (FLUXRIG_BIN_DIR and
		// the test harness's own switches). Only a key the operator wrote in
		// the file is a candidate for a typo.
		if err := validateKnownKeys(k, MixerConfig{}); err != nil {
			return nil, err
		}
	}

	// 3. Environment Variables
	err := k.Load(env.Provider("FLUXRIG_", ".", newEnvKeyMapper(MixerConfig{}, nil)), nil)

	if err != nil {
		return nil, err
	}

	var cfg MixerConfig
	if err := cfg.Unmarshal(k); err != nil {
		return nil, err
	}

	// Fail fast on a duration that does not parse: it would otherwise reach a
	// consumer that discards the parse error and treats zero as real.
	if err := validateDurations(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Registry.validate(); err != nil {
		return nil, err
	}
	if err := cfg.Snake.validateMode(); err != nil {
		return nil, err
	}
	if err := cfg.Snake.validatePings(); err != nil {
		return nil, err
	}
	if err := cfg.Snake.validateAckWait(); err != nil {
		return nil, err
	}
	if err := cfg.Observability.Storage.validate(cfg.Registry.LeaseEnabled); err != nil {
		return nil, err
	}
	if cfg.Snake.External && !cfg.Registry.LeaseEnabled {
		return nil, fmt.Errorf("snake.external needs registry.lease_enabled: Mixers on one NATS must share the write lease")
	}

	// Backward Compatibility: Fallback to old field names
	if cfg.Base.Name == "" {
		cfg.Base.Name = k.String("mixer.mixer_name")
	}
	if cfg.Base.StateDir == "" {
		cfg.Base.StateDir = k.String("store.dir")
	}

	return &cfg, nil
}

// Registry defaults, shared by LoadMixer and WithDefaults.
const (
	DefaultRegistryBucket         = "fluxrig_registry"
	DefaultRegistryLivenessBucket = "fluxrig_liveness"
	DefaultRegistryReplicas       = 1
	DefaultRegistryMaxValueSize   = 1 << 20
	DefaultRegistrySpecBucket     = "fluxrig_specs"
	DefaultRegistrySpecMaxSize    = 1 << 20
	DefaultRegistryLivenessTTL    = "90s"
	DefaultRegistryWriteRetries   = 3
	DefaultRegistrySweepInterval  = "10m"
	DefaultRegistryOrphanMinAge   = "5m"
	DefaultRegistryLeaseBucket    = "fluxrig_lease"
	DefaultRegistryLeaseTTL       = "30s"
	DefaultRegistryLeaseRenew     = "10s"
)

// WithDefaults fills the unset fields whose zero value is never valid with
// their defaults. LoadMixer applies the same defaults; this covers a config
// built in code.
func (r RegistryConfig) WithDefaults() RegistryConfig {
	if r.Bucket == "" {
		r.Bucket = DefaultRegistryBucket
	}
	if r.LivenessBucket == "" {
		r.LivenessBucket = DefaultRegistryLivenessBucket
	}
	if r.Replicas == 0 {
		r.Replicas = DefaultRegistryReplicas
	}
	if r.MaxValueSize == 0 {
		r.MaxValueSize = DefaultRegistryMaxValueSize
	}
	if r.LivenessTTL == "" {
		r.LivenessTTL = DefaultRegistryLivenessTTL
	}
	if r.SweepInterval == "" {
		r.SweepInterval = DefaultRegistrySweepInterval
	}
	if r.OrphanMinAge == "" {
		r.OrphanMinAge = DefaultRegistryOrphanMinAge
	}
	if r.LeaseBucket == "" {
		r.LeaseBucket = DefaultRegistryLeaseBucket
	}
	if r.LeaseTTL == "" {
		r.LeaseTTL = DefaultRegistryLeaseTTL
	}
	if r.LeaseRenew == "" {
		r.LeaseRenew = DefaultRegistryLeaseRenew
	}
	if r.SpecBucket == "" {
		r.SpecBucket = DefaultRegistrySpecBucket
	}
	if r.SpecMaxSize == 0 {
		r.SpecMaxSize = DefaultRegistrySpecMaxSize
	}
	// WriteRetries is not defaulted here: zero is a valid setting (fail on
	// the first conflict), and this runs on loaded configs too.
	return r
}

// validate refuses registry settings that would fail later, at bucket
// creation or on the first write.
func (r RegistryConfig) validate() error {
	if r.Bucket == "" || r.LivenessBucket == "" || r.Bucket == r.LivenessBucket {
		return fmt.Errorf("registry.bucket and registry.liveness_bucket must be set and differ")
	}
	if r.Replicas < 1 {
		return fmt.Errorf("registry.replicas must be at least 1, got %d", r.Replicas)
	}
	if r.MaxValueSize < 1 || r.MaxValueSize > math.MaxInt32 {
		return fmt.Errorf("registry.max_value_size must be between 1 and %d, got %d", math.MaxInt32, r.MaxValueSize)
	}
	if r.WriteRetries < 0 {
		return fmt.Errorf("registry.write_retries must not be negative, got %d", r.WriteRetries)
	}
	if d, err := time.ParseDuration(r.LivenessTTL); err != nil || d <= 0 {
		return fmt.Errorf("registry.liveness_ttl must be a positive duration, got %q", r.LivenessTTL)
	}
	if d, err := time.ParseDuration(r.SweepInterval); err != nil || d <= 0 {
		return fmt.Errorf("registry.sweep_interval must be a positive duration, got %q", r.SweepInterval)
	}
	// The grace period covers a write sequence in flight. It has to be
	// positive, or the sweep could undo a registration still in progress.
	if d, err := time.ParseDuration(r.OrphanMinAge); err != nil || d <= 0 {
		return fmt.Errorf("registry.orphan_min_age must be a positive duration, got %q", r.OrphanMinAge)
	}
	if r.SpecMaxSize < 1 || r.SpecMaxSize > math.MaxInt32 {
		return fmt.Errorf("registry.spec_max_size must be between 1 and %d, got %d", math.MaxInt32, r.SpecMaxSize)
	}
	if r.LeaseEnabled {
		if r.SpecBucket == "" || r.SpecBucket == r.Bucket || r.SpecBucket == r.LivenessBucket || r.SpecBucket == r.LeaseBucket {
			return fmt.Errorf("registry.spec_bucket must be set and differ from the registry, liveness and lease buckets")
		}
		if r.LeaseBucket == "" || r.LeaseBucket == r.Bucket || r.LeaseBucket == r.LivenessBucket {
			return fmt.Errorf("registry.lease_bucket must be set and differ from the registry and liveness buckets")
		}
		ttl, err := time.ParseDuration(r.LeaseTTL)
		if err != nil || ttl <= 0 {
			return fmt.Errorf("registry.lease_ttl must be a positive duration, got %q", r.LeaseTTL)
		}
		renew, err := time.ParseDuration(r.LeaseRenew)
		if err != nil || renew <= 0 {
			return fmt.Errorf("registry.lease_renew must be a positive duration, got %q", r.LeaseRenew)
		}
		// Validation runs after the defaults are applied, so both values are
		// known here. A renew interval of half the TTL or more leaves room for
		// at most one renew attempt before the lease lapses, so one slow renew
		// hands the lease to another Mixer.
		if renew > ttl/2 {
			return fmt.Errorf("registry.lease_renew (%s) must be at most half of registry.lease_ttl (%s)", renew, ttl)
		}
	}
	return nil
}

// validate refuses a telemetry storage that cannot work.
func (s StorageConfig) validate(leaseEnabled bool) error {
	switch s.Type {
	case StorageLocal:
		return nil
	case StorageS3:
		if s.S3.Endpoint == "" || s.S3.Bucket == "" {
			return fmt.Errorf("observability.storage.type s3 needs observability.storage.s3.endpoint and bucket")
		}
	case StorageNATS:
		if s.NATS.Bucket == "" {
			return fmt.Errorf("observability.storage.type nats needs observability.storage.nats.bucket")
		}
	case StorageOTLP:
		if s.OTLP.Endpoint == "" {
			return fmt.Errorf("observability.storage.type otlp needs observability.storage.otlp.endpoint")
		}
	default:
		return fmt.Errorf("observability.storage.type must be local, s3, nats or otlp, got %q", s.Type)
	}
	// One writer: the Mixer that holds the lease.
	if !leaseEnabled {
		return fmt.Errorf("observability.storage.type %s needs registry.lease_enabled: the Mixer that holds the lease is the one that writes", s.Type)
	}
	for _, in := range []struct{ name, val string }{
		{"observability.storage.commit_timeout", s.CommitTimeout},
		{"observability.storage.batch_interval", s.BatchInterval},
	} {
		if d, err := time.ParseDuration(in.val); err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %q", in.name, in.val)
		}
	}
	return nil
}

// validateMode refuses a setup that would fail later, at startup or at the
// first replicated write. The embedded server runs alone and holds one copy of
// each stream, so more copies need snake.external.
func (s SnakeConfig) validateMode() error {
	if d, err := time.ParseDuration(s.FormTimeout); err != nil || d <= 0 {
		return fmt.Errorf("snake.form_timeout must be a positive duration, got %q", s.FormTimeout)
	}
	if s.Replicas < 1 {
		return fmt.Errorf("snake.replicas must be at least 1, got %d", s.Replicas)
	}
	if s.External {
		if s.URL == "" {
			return fmt.Errorf("snake.external needs snake.url")
		}
		return nil
	}
	if s.Replicas > 1 {
		return fmt.Errorf("snake.replicas is %d, and the embedded NATS runs alone with one copy: more than one needs snake.external, on a NATS cluster", s.Replicas)
	}
	return nil
}
