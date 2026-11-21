// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"
)

// BaseConfig defines foundational identity and persistence paths.
type BaseConfig struct {
	Name      string `koanf:"name" example:"node-01"`
	StateDir  string `koanf:"state_dir" example:"./data"`
	StateFile string `koanf:"state_file" example:"rack.flux"`
	// ScenarioFile names, inside StateDir, the Rack's copy of the last scenario it applied. Empty keeps no copy.
	ScenarioFile string `koanf:"scenario_file" example:"scenario.flux"`
}

// LoggingConfig configures the structured logger.
type LoggingConfig struct {
	Level      string           `koanf:"level" example:"info"`
	Filename   string           `koanf:"filename" example:"logs/fluxrig.log"`
	MaxSizeMB  int              `koanf:"max_size_mb" example:"100"`
	MaxBackups int              `koanf:"max_backups" example:"7"`
	Compress   bool             `koanf:"compress" example:"true"`
	Trace      bool             `koanf:"trace"` // Hard-override for trace level
	Debug      bool             `koanf:"debug"` // Hard-override for debug level
	Throttling ThrottlingConfig `koanf:"throttling"`
}

// ThrottlingConfig limits log volume to prevent starvation.
type ThrottlingConfig struct {
	Enabled bool    `koanf:"enabled" example:"true"`
	Rate    float64 `koanf:"rate" example:"500.0"`
	Burst   int     `koanf:"burst" example:"50"`
}

// StoreConfig configures persistent storage locations.
type StoreConfig struct {
	Dir            string `koanf:"dir" example:"./data"`
	WALMaxSizeMB   int    `koanf:"wal_max_size_mb" example:"500"`
	DatabaseFile   string `koanf:"database_file" example:"flux.duckdb"`
	ClusterKeyFile string `koanf:"cluster_key_file" example:"cluster.key"`
}

// The client pings of NATS: a server that went silent is noticed within
// about DefaultPingInterval times DefaultMaxPingsOut. The NATS client
// defaults (2m, 2) took minutes, while every request timed out.
const (
	DefaultPingInterval = "5s"
	DefaultMaxPingsOut  = 3
	// DefaultAckWait is how long a consumer waits for the acknowledgement of
	// a message before it delivers the message again. The JetStream default
	// is 30s.
	DefaultAckWait = "10s"
)

// SnakeConfig configures the NATS/JetStream connection (The Snake).
type SnakeConfig struct {
	URL    string `koanf:"url" example:"nats://localhost:4222"`
	Port   int    `koanf:"port" example:"4222"`
	Domain string `koanf:"domain" example:"flux"`
	// ClusterName is the JetStream domain the embedded server reports. It is
	// also what a client uses to reach a domain-scoped server.
	ClusterName string `koanf:"cluster_name" example:"flux"`
	StreamName  string `koanf:"stream_name" example:"flux-msg"`
	// Replicas is how many copies of every stream and bucket the Mixer
	// provisions. The embedded server runs alone and holds one, so more than
	// 1 needs snake.external, on a NATS cluster with at least that many servers.
	Replicas int `koanf:"replicas" example:"1"`
	// External means that url names a NATS cluster the operator runs. A Mixer
	// starts no embedded server, and several Mixers on one cluster give HA. A
	// client verifies the host of the URL and not the domain, on a Mixer and on
	// a Rack.
	External bool `koanf:"external" example:"false"`
	// ClientCertFile and ClientKeyFile are the certificate this client presents
	// to NATS. CredentialsFile is a NATS credentials file, with the permissions
	// of this client.
	ClientCertFile  string `koanf:"client_cert_file"`
	ClientKeyFile   string `koanf:"client_key_file"`
	CredentialsFile string `koanf:"credentials_file"`
	// PingInterval and MaxPingsOut decide how fast a client of NATS, a Mixer
	// or a Rack, notices a server that went silent: after MaxPingsOut
	// unanswered pings sent PingInterval apart it reconnects to another
	// server. A server that dies without closing its connections (a crash, a
	// cut link) is noticed only so, and until then every request on the
	// connection times out.
	PingInterval string `koanf:"ping_interval" example:"5s"`
	MaxPingsOut  int    `koanf:"max_pings_out" example:"3"`
	// AckWait is how long a consumer of the guaranteed lane, and a durable
	// one, waits for a message's acknowledgement before it delivers it again.
	// A message sent through a NATS server that died arrives that late. It
	// must be longer than OperationTimeout, which bounds the processing of a
	// message by a gear: shorter, a slow gear gets each message twice.
	AckWait string `koanf:"ack_wait" example:"10s"`
	// FormTimeout is how long a Mixer on an external NATS waits for it to
	// answer, and for a replicated stream to become placeable, before it gives
	// up starting. Generous by default: an operator restarting a NATS cluster
	// brings its servers up one after another.
	FormTimeout string `koanf:"form_timeout" example:"2m"`
	// StoreDir overrides where the embedded Snake keeps its JetStream store.
	// Empty keeps the default: a "snake" directory under store.dir.
	StoreDir                  string   `koanf:"store_dir" example:"./data/snake"`
	ConnectTimeout            string   `koanf:"connect_timeout" example:"10s"`
	ReconnectWait             string   `koanf:"reconnect_wait" example:"1s"`
	OperationTimeout          string   `koanf:"operation_timeout" example:"5s"`
	SubscriptionRetryWait     string   `koanf:"subscription_retry_wait" example:"200ms"`
	SubscriptionRetryAttempts int      `koanf:"subscription_retry_attempts" example:"5"`
	InactiveThreshold         string   `koanf:"inactive_threshold" example:"30s"`
	ConvergenceDelay          string   `koanf:"convergence_delay" example:"100ms"`
	InitialRetryWait          string   `koanf:"initial_retry_wait" example:"500ms"`
	InitialRetryAttempts      int      `koanf:"initial_retry_attempts" example:"10"`
	OfflineStartTimeout       string   `koanf:"offline_start_timeout" example:"3s"`
	OfflineRetryInterval      string   `koanf:"offline_retry_interval" example:"5s"`
	StoreEncryption           bool     `koanf:"store_encryption" example:"true"`
	StoreCipher               string   `koanf:"store_cipher" example:"chacha"`
	StoreKeyFile              string   `koanf:"store_key_file"`
	StoreOldKeyFile           string   `koanf:"store_old_key_file"`
	StreamMaxAge              string   `koanf:"stream_max_age" example:"24h"`
	StreamMaxBytes            int64    `koanf:"stream_max_bytes" example:"1073741824"`
	RootCAFile                string   `koanf:"root_ca_file"`
	TLSCertFile               string   `koanf:"tls_cert_file"`
	TLSKeyFile                string   `koanf:"tls_key_file"`
	InsecureSkipVerify        bool     `koanf:"insecure_skip_verify"`
	StreamSubjects            []string `koanf:"stream_subjects"`
	// AllowNonTLS lets a plaintext client connect alongside a TLS one once
	// TLSCertFile/TLSKeyFile are set. Mixer-side only: false by default, so
	// configuring TLS actually requires it instead of just offering it.
	AllowNonTLS bool `koanf:"allow_non_tls" example:"false"`
	// TLSCAFile and TLSVerify enable mTLS: the CA a Rack's own client
	// certificate is verified against, and whether presenting one is
	// required. Mixer-side only.
	TLSCAFile string `koanf:"tls_ca_file"`
	TLSVerify bool   `koanf:"tls_verify" example:"false"`
	// KVMaxBytes, KVMaxValueSize and KVTTL cap every KV bucket the Mixer
	// provisions, the same way StreamMaxAge/StreamMaxBytes cap every stream.
	// Mixer-side only.
	KVMaxBytes     int64  `koanf:"kv_max_bytes" example:"1073741824"`
	KVMaxValueSize int32  `koanf:"kv_max_value_size" example:"1048576"`
	KVTTL          string `koanf:"kv_ttl" example:"0s"`
	// DLQEnabled routes unprocessable messages (decode failure, validation
	// failure, or a handler that failed on every redelivery) to the dead
	// letter stream instead of dropping them with only a log line. Off keeps
	// the previous behavior: log and discard.
	DLQEnabled bool `koanf:"dlq_enabled" example:"true"`
	// DLQSubject is the subject prefix the dead letter stream captures.
	DLQSubject string `koanf:"dlq_subject" example:"flux.dlq"`
	// DLQMaxAge bounds how long a dead letter is kept. Empty keeps the
	// stream default (24h).
	DLQMaxAge string `koanf:"dlq_max_age" example:"720h"`
	// DLQPublishTimeout bounds how long one dead letter publish waits for the
	// server. A dead letter that cannot be written in time is logged and the
	// consumer moves on, so a stalled server does not hold it.
	DLQPublishTimeout string `koanf:"dlq_publish_timeout" example:"5s"`
	// EventsMaxAge and EventsMaxBytes bound the stream of durable events. A
	// reader that was down longer than the age has lost what it missed.
	// Mixer-side only.
	EventsMaxAge   string `koanf:"events_max_age" example:"1h"`
	EventsMaxBytes int64  `koanf:"events_max_bytes" example:"268435456"`
	// FailoverAfter is how many JetStream publishes in a row may find no
	// stream (a timeout or no responders) before a Rack drops its Mixer and
	// reconnects to another one in snake.url. A Mixer cut off from its cluster
	// keeps the Rack's connection open while every publish through it fails.
	// 0 disables it. Rack-side only.
	FailoverAfter int `koanf:"failover_after" example:"2"`
	// ConsumerReplicas is the replica count of the consumer behind each
	// acknowledged subscription (a guaranteed-lane wire, a scenario push).
	// 0 takes the stream's replica count, so in a cluster the consumer
	// survives the loss or the partition of a Mixer. 1 keeps one replica,
	// lighter on the Mixers. Rack-side only.
	ConsumerReplicas int `koanf:"consumer_replicas" example:"0"`
	// ConsumerMissedHeartbeats is how many heartbeats in a row the consumer
	// of a subscription may miss before the Rack replaces it, while the
	// cluster still lists it or cannot answer. A heartbeat is due every
	// operation_timeout. Rack-side only.
	ConsumerMissedHeartbeats int `koanf:"consumer_missed_heartbeats" example:"2"`
}

// TelemetryConfig configures self-reporting metrics and logs.
type TelemetryConfig struct {
	ServiceName    string        `koanf:"service_name"`
	BatchInterval  string        `koanf:"batch_interval"`
	BaseSubject    string        `koanf:"base_subject"`
	MaxBatchSize   int           `koanf:"max_batch_size"`
	StreamName     string        `koanf:"stream_name"`
	StreamSubjects []string      `koanf:"stream_subjects"`
	Disabled       bool          `koanf:"disabled"` // Master switch to disable all telemetry
	Metrics        MetricsConfig `koanf:"metrics"`
}

// MetricsConfig toggles specific telemetry groups.
type MetricsConfig struct {
	HostEnabled    bool `koanf:"host_enabled"`
	RuntimeEnabled bool `koanf:"runtime_enabled"`
	BentoEnabled   bool `koanf:"bento_enabled"`
}

// validatePings refuses ping settings that would leave the client on the
// NATS client defaults, which take minutes to notice a silent server.
func (s SnakeConfig) validatePings() error {
	if d, err := time.ParseDuration(s.PingInterval); err != nil || d <= 0 {
		return fmt.Errorf("snake.ping_interval must be a positive duration, got %q", s.PingInterval)
	}
	if s.MaxPingsOut < 1 {
		return fmt.Errorf("snake.max_pings_out must be at least 1, got %d", s.MaxPingsOut)
	}
	return nil
}

// validateAckWait refuses an ack wait that is not longer than the processing
// of one message, which would deliver every slow message again.
func (s SnakeConfig) validateAckWait() error {
	wait, err := time.ParseDuration(s.AckWait)
	if err != nil || wait <= 0 {
		return fmt.Errorf("snake.ack_wait must be a positive duration, got %q", s.AckWait)
	}
	if op, err := time.ParseDuration(s.OperationTimeout); err == nil && wait <= op {
		return fmt.Errorf("snake.ack_wait (%s) must be longer than snake.operation_timeout (%s), which bounds the processing of one message", s.AckWait, s.OperationTimeout)
	}
	return nil
}
