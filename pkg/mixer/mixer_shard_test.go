// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestRun_ErrorPaths(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "mixer_run_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("InvalidStoreDir", func(t *testing.T) {
		// A directory below a regular file cannot be created by any user. A missing
		// path under the root of the disk can, when the tests run as root.
		blocker := filepath.Join(tmpDir, "blocker")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))
		cfg := &config.MixerConfig{
			Store: config.StoreConfig{
				Dir: filepath.Join(blocker, "store"),
			},
		}
		app := NewApp(cfg, "", nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := app.Run(ctx)
		assert.Error(t, err)
	})

	t.Run("ZeroConfigKeyGeneration", func(t *testing.T) {
		subDir := filepath.Join(tmpDir, "zero-config")
		_ = os.MkdirAll(subDir, 0750)

		cfg := &config.MixerConfig{
			Store: config.StoreConfig{
				Dir:            subDir,
				DatabaseFile:   "mixer.db",
				ClusterKeyFile: "generated.key",
			},
			Snake: config.SnakeConfig{
				Port: -1,
			},
			API: config.ApiConfig{
				Port: 0,
			},
		}
		app := NewApp(cfg, "", nil)

		// The key is generated after the store migrates, and a migration under
		// -race on a loaded machine can outlast any fixed run budget. Poll for the
		// key, then stop the Mixer.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = app.Run(ctx)
		}()

		keyPath := filepath.Join(subDir, "generated.key")
		assert.Eventually(t, func() bool {
			_, statErr := os.Stat(keyPath)
			return statErr == nil
		}, 30*time.Second, 20*time.Millisecond, "Cluster key should have been auto-generated")

		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("Mixer did not stop after its context was cancelled")
		}
	})

	t.Run("StoreInitFailure", func(t *testing.T) {
		dbPath := filepath.Join(tmpDir, "mixer.db")
		_ = os.MkdirAll(dbPath, 0750)

		keyPath := filepath.Join(tmpDir, "cluster.key")
		ck, _ := pki.GenerateClusterKey()
		_ = ck.Save(keyPath)

		cfg := &config.MixerConfig{
			Store: config.StoreConfig{
				Dir:            tmpDir,
				DatabaseFile:   "mixer.db",
				ClusterKeyFile: "cluster.key",
			},
		}
		app := NewApp(cfg, "", nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := app.Run(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to init store")
	})
}
