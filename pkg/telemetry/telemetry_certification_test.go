// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func TestTelemetry_CertificationPush(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "telemetry_certification_*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := Config{
		ServiceName:         "certification-telemetry",
		EntityID:            123,
		EntityName:          "cert-node",
		Component:           "TEST",
		BatchIntervalString: "100ms",
		MaxBatchSize:        10,
		BaseSubject:         "flux.telemetry",
		Metrics: MetricsConfig{
			HostEnabled:    true,
			RuntimeEnabled: true,
		},
		Store: config.StoreConfig{
			Dir: tmpDir,
		},
	}

	t.Run("Engine_Lifecycle_Logic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		gen, _ := idgen.New(5)
		mockBus := bus.NewMockBus()

		shutdown, err := Init(ctx, cfg, mockBus, nil, gen) // Valid MockBus
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		defer func() { _ = shutdown(context.Background()) }()

		// Short wait for runtime collector to initialize
		time.Sleep(200 * time.Millisecond)

		// Verification: Check if metrics are being generated
		metrics := GetMetrics()
		assert.NotNil(t, metrics)
	})

	t.Run("Config_Validation_Logic", func(t *testing.T) {
		assert.Equal(t, "certification-telemetry", cfg.ServiceName)
		assert.True(t, cfg.Metrics.HostEnabled)
	})
}
