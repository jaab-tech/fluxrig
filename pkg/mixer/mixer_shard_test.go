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

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestRun_ErrorPaths(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "mixer_run_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("InvalidStoreDir", func(t *testing.T) {
		cfg := &config.MixerConfig{
			Store: config.StoreConfig{
				Dir: "/nonexistent/path/that/fails/mkdir",
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

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		_ = app.Run(ctx)

		keyPath := filepath.Join(subDir, "generated.key")
		_, statErr := os.Stat(keyPath)
		assert.NoError(t, statErr, "Cluster key should have been auto-generated")
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
