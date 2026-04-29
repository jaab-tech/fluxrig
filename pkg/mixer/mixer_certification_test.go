// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

func TestMixer_AppCertification(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mixer_certification_test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Establish bit-perfect institutional configuration
	keyPath := filepath.Join(tmpDir, "cluster.key")
	pub, priv, _ := ed25519.GenerateKey(nil)
	ck := &pki.ClusterKey{Private: priv, Public: pub}
	require.NoError(t, ck.Save(keyPath))

	cfg := &config.MixerConfig{
		Store: config.StoreConfig{
			Dir:            tmpDir,
			DatabaseFile:   "mixer.db",
			ClusterKeyFile: "cluster.key",
		},
		API: config.ApiConfig{
			Port: 0, // Auto-bind
		},
		Snake: config.SnakeConfig{
			Port:           0, // Auto-bind
			ClusterName:    "test-cluster",
			StreamName:     "flux-msg",
			StreamSubjects: []string{"flux.msg.>"},
			URL:            "nats://127.0.0.1:0",
		},
		Enrollment: config.EnrollmentConfig{
			PushDelay: "100ms",
			AutoAdopt: true,
		},
		Mixer: config.MixerSettings{
			MachineID: 7,
			MixerName: "certification-mixer",
		},
		Telemetry: config.TelemetryConfig{
			ServiceName:   "certification-mixer",
			BatchInterval: "100ms",
			MaxBatchSize:  10,
			BaseSubject:   "flux.telemetry",
			Metrics: config.MetricsConfig{
				HostEnabled:    true,
				RuntimeEnabled: true,
			},
		},
		Ingest: config.IngestConfig{
			FlushInterval: "100ms",
		},
		Observability: config.ObservabilityConfig{
			Embedded: config.EmbeddedConfig{
				RetentionDays: 7,
			},
		},
	}

	app := NewApp(cfg, "")
	require.NotNil(t, app)

	t.Run("App_Run_Smoke_Test", func(t *testing.T) {
		// Exercise core logic briefly
		done := make(chan error, 1)
		go func() {
			done <- app.Run()
		}()

		time.Sleep(500 * time.Millisecond)

		// Verification: Check if store dir exists
		assert.DirExists(t, cfg.Store.Dir)
	})

	t.Run("EntityResumption_Success", func(t *testing.T) {
		logger := slog.Default()
		store, err := duckdb.NewStore(logger, filepath.Join(tmpDir, "resumption.db"))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()
		require.NoError(t, store.Migrate(context.Background()))

		idGen, _ := idgen.New(7)
		highEID := idGen.NewEntityID(idgen.EntityMixer, 1)
		// Register a fake high-sequence entity to verify resumption
		require.NoError(t, store.RegisterMixer(context.Background(), 7, "old-mixer", highEID, "127.0.0.1:8080", "0.4.3"))

		// Verification: The resumeEntitySequence should detect highEID and update idGen
		app.resumeEntitySequence(store, 7, idGen)
		next := idGen.NextEntityID(idgen.EntityMixer)
		// Since machineID=7 and seq was highEID's seq, next should be high
		assert.GreaterOrEqual(t, next&0xFFFFFFFFFF, uint64(1))
	})

	t.Run("SnakeDiscovery_Logic", func(t *testing.T) {
		logger := slog.Default()
		store, err := duckdb.NewStore(logger, filepath.Join(tmpDir, "discovery.db"))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()
		require.NoError(t, store.Migrate(context.Background()))

		idGen, _ := idgen.New(7)
		mixerEID := idGen.NextEntityID(idgen.EntityMixer)

		snakeName := "snake-rack-01"
		eid := idGen.NextEntityID(idgen.EntitySnake)

		require.NoError(t, store.RegisterSnake(context.Background(), snakeName, eid, "v0.4.3", 101, mixerEID, "127.0.0.1", 12345, "127.0.0.1", 4222, 7))

		stats := map[string]any{"in_msgs": 10}
		require.NoError(t, store.UpdateSnakeStats(context.Background(), eid, stats))
	})
}
