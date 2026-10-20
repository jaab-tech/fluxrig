// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
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
			// The Mixer refuses to start without an API token; a test runs
			// with auth disabled, as the e2e and Robot harnesses do.
			AuthDisabledDangerously: true,
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
		Wasm: config.WasmConfig{
			CatalogDir:     filepath.Join(tmpDir, "wasm"),
			TrustedKeysDir: filepath.Join(tmpDir, "wasm", "keys"),
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

		// Wait for startup to finish, not for a fixed time: cancelling
		// mid-startup makes Run return the cancellation (for example from the
		// store migration) and fails the test on a slow machine.
		select {
		case <-app.Ready():
		case err := <-done:
			t.Fatalf("Mixer exited during startup: %v", err)
		case <-time.After(60 * time.Second):
			t.Fatal("Timeout waiting for Mixer startup")
		}

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
}
