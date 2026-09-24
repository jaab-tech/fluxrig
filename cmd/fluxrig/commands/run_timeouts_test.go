// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

func validTimeouts() *config.RackConfig {
	cfg := &config.RackConfig{}
	cfg.Rack.CleanupTimeout = "2s"
	cfg.Rack.DrainTimeout = "35s"
	cfg.Rack.ConvergenceTimeout = "5s"
	cfg.Rack.HandshakeInterval = "500ms"
	cfg.Snake.SubscriptionRetryWait = "200ms"
	cfg.Snake.OperationTimeout = "5s"
	cfg.Snake.InitialRetryWait = "500ms"
	cfg.Snake.OfflineStartTimeout = "3s"
	cfg.Snake.OfflineRetryInterval = "5s"
	cfg.Rack.LaneSendTimeout = "5s"
	return cfg
}

func TestParseRackTimeouts(t *testing.T) {
	got, err := parseRackTimeouts(validTimeouts())

	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, got.cleanup)
	assert.Equal(t, 35*time.Second, got.drain)
	assert.Equal(t, 5*time.Second, got.convergence)
	assert.Equal(t, 500*time.Millisecond, got.handshake)
	assert.Equal(t, 200*time.Millisecond, got.subscriptionRetry)
	assert.Equal(t, 5*time.Second, got.operation)
	assert.Equal(t, 500*time.Millisecond, got.initialRetryWait)
	assert.Equal(t, 3*time.Second, got.offlineStart)
	assert.Equal(t, 5*time.Second, got.offlineRetry)
	assert.Equal(t, 5*time.Second, got.laneSend)
}

// "0s" is not a typo: it is the documented sentinel that turns off the initial-retry
// bound, the offline probe or the lane send bound. It must keep parsing to zero, not
// be caught by the "every setting fails fast" case below.
func TestParseRackTimeouts_ZeroDisablesOnPurpose(t *testing.T) {
	cfg := validTimeouts()
	cfg.Snake.InitialRetryWait = "0s"
	cfg.Snake.OfflineStartTimeout = "0s"
	cfg.Snake.OfflineRetryInterval = "0s"
	cfg.Rack.LaneSendTimeout = "0s"

	got, err := parseRackTimeouts(cfg)

	require.NoError(t, err)
	assert.Zero(t, got.initialRetryWait)
	assert.Zero(t, got.offlineStart)
	assert.Zero(t, got.offlineRetry)
	assert.Zero(t, got.laneSend)
}

// Each setting that is not a duration stops the start and is named in the error. A
// typo in any of these four used to be silently read as zero instead: the initial
// retry ran with no wait, the offline probe fired as fast as it could loop, and the
// lane send bound vanished, none of it reported.
func TestParseRackTimeouts_NamesTheSettingThatIsWrong(t *testing.T) {
	cases := map[string]func(*config.RackConfig){
		"rack.cleanup_timeout":          func(c *config.RackConfig) { c.Rack.CleanupTimeout = "soon" },
		"rack.drain_timeout":            func(c *config.RackConfig) { c.Rack.DrainTimeout = "soon" },
		"rack.convergence_timeout":      func(c *config.RackConfig) { c.Rack.ConvergenceTimeout = "soon" },
		"rack.handshake_interval":       func(c *config.RackConfig) { c.Rack.HandshakeInterval = "soon" },
		"snake.subscription_retry_wait": func(c *config.RackConfig) { c.Snake.SubscriptionRetryWait = "soon" },
		"snake.operation_timeout":       func(c *config.RackConfig) { c.Snake.OperationTimeout = "soon" },
		"snake.initial_retry_wait":      func(c *config.RackConfig) { c.Snake.InitialRetryWait = "soon" },
		"snake.offline_start_timeout":   func(c *config.RackConfig) { c.Snake.OfflineStartTimeout = "soon" },
		"snake.offline_retry_interval":  func(c *config.RackConfig) { c.Snake.OfflineRetryInterval = "soon" },
		"rack.lane_send_timeout":        func(c *config.RackConfig) { c.Rack.LaneSendTimeout = "soon" },
	}
	for setting, break_ := range cases {
		t.Run(setting, func(t *testing.T) {
			cfg := validTimeouts()
			break_(cfg)

			_, err := parseRackTimeouts(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), setting)
		})
	}
}
