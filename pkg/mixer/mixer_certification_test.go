// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"

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

	testID := uuid.New().String()[:8]

	cfg := &config.MixerConfig{
		Base: config.BaseConfig{
			Name:     "certification-mixer-" + testID,
			StateDir: tmpDir,
		},
		Store: config.StoreConfig{
			Dir:            tmpDir,
			DatabaseFile:   "mixer.db",
			ClusterKeyFile: "cluster.key",
		},
		API: config.ApiConfig{
			Port: 0, // Auto-bind
		},
		Snake: config.SnakeConfig{
			Port:       -1, // Random port
			Domain:     "cert-cluster-" + testID,
			StreamName: "cert-msg-" + testID,
			URL:        "nats://127.0.0.1:0",
		},
		Enrollment: config.EnrollmentConfig{
			PushDelay: "100ms",
			AutoAdopt: true,
		},
		Mixer: config.MixerSettings{
			// Empty for now
		},
		Telemetry: config.TelemetryConfig{
			ServiceName:   "cert-mixer-" + testID,
			BatchInterval: "100ms",
			MaxBatchSize:  10,
			BaseSubject:   fmt.Sprintf("cert.telemetry.%s", testID),
			StreamName:    "cert-telemetry-" + testID,
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

	app := NewApp(cfg, "", nil)
	require.NotNil(t, app)

	t.Run("App_Run_Smoke_Test", func(t *testing.T) {
		// Exercise core logic briefly
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- app.Run(ctx)
		}()

		time.Sleep(2 * time.Second)

		// Verification: Check if store dir exists
		assert.DirExists(t, cfg.Store.Dir)

		// Shutdown
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("Timeout waiting for Mixer shutdown")
		}
	})

	t.Run("SnakeDiscovery_Logic", func(t *testing.T) {
		logger := slog.Default()
		store, err := duckdb.NewStore(logger, filepath.Join(tmpDir, "discovery.db"))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()
		require.NoError(t, store.Migrate(context.Background()))

		idGen, _ := idgen.New(uuid.New())
		mixerEID := idGen.NextEntityID(idgen.EntityMixer)

		snakeName := "snake-rack-01"
		eid := idGen.NextEntityID(idgen.EntitySnake)

		require.NoError(t, store.RegisterSnake(context.Background(), snakeName, eid, "v0.4.3", uuid.New(), mixerEID, "127.0.0.1", 12345, "127.0.0.1", 4222, uuid.New()))

		stats := map[string]any{"in_msgs": 10}
		require.NoError(t, store.UpdateSnakeStats(context.Background(), eid, stats))
	})
}
