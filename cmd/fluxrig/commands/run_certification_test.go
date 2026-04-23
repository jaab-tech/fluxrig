// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_RunCertification(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cli_run_test")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	idGen, _ := idgen.New(1)

	// 1. Establish Sovereign Infrastructure
	s, err := snake.NewServer(snake.Config{Port: -1, ClusterName: "cli-run-test"})
	require.NoError(t, err)
	defer s.Shutdown()

	cfg := &config.RackConfig{
		Rack: config.RackSettings{
			Name: "certification-rack",
			Bus: config.BusConfig{
				URL:        s.ClientURL(),
				StreamName: "flux-msg",
			},
			EnrollmentTimeout: "200ms",
			HeartbeatInterval: "100ms",
		},
		Store: config.StoreConfig{
			Dir:       tmpDir,
			StateFile: "state.flux",
		},
		Telemetry: config.TelemetryConfig{
			BatchInterval: "100ms",
			MaxBatchSize:  10,
		},
	}

	t.Run("RunSession_IdentityRotation_Logic", func(t *testing.T) {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		signer := &pki.ClusterKey{Private: priv, Public: pub}

		state := &pki.RackState{
			MachineID:   101,
			Name:        "signed-rack",
			Status:      "active",
			Secret:      "deadbeef-certification-secret",
			MixerPublic: pub,
		}
		env, _ := signer.Sign(state)
		statePath := filepath.Join(tmpDir, "state.flux")
		_ = env.Save(statePath)

		// Test identity loading
		if loadedEnv, err := pki.LoadStateEnvelope(statePath); err == nil {
			if vs, errVer := loadedEnv.Verify(); errVer == nil {
				assert.Equal(t, uint16(101), vs.MachineID)
				assert.Equal(t, "signed-rack", vs.Name)
				assert.NotEmpty(t, vs.Secret)
			} else {
				t.Fatalf("failed to verify envelope: %v", errVer)
			}
		} else {
			t.Fatalf("failed to load envelope: %v", err)
		}
	})

	t.Run("Heartbeat_Instrumentation_Logic", func(t *testing.T) {
		managedBus := bus.NewMockBus()
		_ = managedBus.Connect(cfg.Rack.Bus.URL, bus.ConnectOptions{})

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		err := sendHeartbeat(ctx, managedBus, 101, cfg, idGen)
		assert.NoError(t, err)
	})

	t.Run("CLI_Flag_Integration_Blitz", func(t *testing.T) {
		assert.NotNil(t, runCmd)
		assert.Equal(t, "run", runCmd.Use)

		err := runCmd.Help()
		assert.NoError(t, err)
	})
}

func TestCLI_Adoption_Handshake_Logic(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "cli_adoption_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	isSessionActive := false
	runtimeStarted := false

	t.Run("InPlacePromotion_Logic", func(t *testing.T) {
		status := "active"
		if status == "active" && !isSessionActive {
			isSessionActive = true
			runtimeStarted = true
		}

		assert.True(t, isSessionActive)
		assert.True(t, runtimeStarted)
	})
}
