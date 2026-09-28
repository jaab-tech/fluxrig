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
	cfg.Snake.ConnectTimeout = "10s"
	cfg.Snake.ReconnectWait = "1s"
	cfg.Rack.EnrollmentTimeout = "15s"
	cfg.Rack.EnrollmentInterval = "2s"
	cfg.Rack.HeartbeatInterval = "30s"
	cfg.Snake.InactiveThreshold = "30s"
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
	assert.Equal(t, 10*time.Second, got.connect)
	assert.Equal(t, 1*time.Second, got.reconnectWait)
	assert.Equal(t, 15*time.Second, got.enrollTimeout)
	assert.Equal(t, 2*time.Second, got.enrollInterval)
	assert.Equal(t, 30*time.Second, got.heartbeat)
	assert.Equal(t, 30*time.Second, got.inactiveThreshold)
}

// "0s" is not a typo: it is the documented sentinel that turns off the initial-retry
// bound, the offline probe or the lane send bound. It must keep parsing to zero, not
// be caught by the "every setting fails fast" case below, and not be clamped to a
// default the way a genuinely accidental zero elsewhere in this file now is.
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

// TestParseRackTimeouts_ClampsNonPositiveToDefault is a regression test for a real
// bug: parseRackTimeouts checked err != nil but never <= 0, so a "0s" (or a
// negative value) in any of these twelve settings was accepted and silently used as
// the bound itself. Four of them (heartbeat, enrollInterval, handshake, plus the
// ticker inside operation's own downstream use) reach a time.NewTicker/.NewTimer and
// either panic outright or fire on the very first tick; the rest fire the wait they
// bound instantly. All twelve now clamp to their documented default from
// pkg/config's LoadRack instead. This is a different set of settings than the
// four in TestParseRackTimeouts_ZeroDisablesOnPurpose above, which are a
// documented intentional off-switch and must NOT be clamped.
func TestParseRackTimeouts_ClampsNonPositiveToDefault(t *testing.T) {
	cases := map[string]struct {
		mutate func(*config.RackConfig, string)
		want   time.Duration
	}{
		"rack.cleanup_timeout":          {func(c *config.RackConfig, v string) { c.Rack.CleanupTimeout = v }, defaultCleanupTimeout},
		"rack.drain_timeout":            {func(c *config.RackConfig, v string) { c.Rack.DrainTimeout = v }, defaultDrainTimeout},
		"rack.convergence_timeout":      {func(c *config.RackConfig, v string) { c.Rack.ConvergenceTimeout = v }, defaultConvergenceTimeout},
		"rack.handshake_interval":       {func(c *config.RackConfig, v string) { c.Rack.HandshakeInterval = v }, defaultHandshakeInterval},
		"snake.subscription_retry_wait": {func(c *config.RackConfig, v string) { c.Snake.SubscriptionRetryWait = v }, defaultSubscriptionRetry},
		"snake.operation_timeout":       {func(c *config.RackConfig, v string) { c.Snake.OperationTimeout = v }, defaultOperationTimeout},
		"snake.connect_timeout":         {func(c *config.RackConfig, v string) { c.Snake.ConnectTimeout = v }, defaultConnectTimeout},
		"snake.reconnect_wait":          {func(c *config.RackConfig, v string) { c.Snake.ReconnectWait = v }, defaultReconnectWait},
		"rack.enrollment_timeout":       {func(c *config.RackConfig, v string) { c.Rack.EnrollmentTimeout = v }, defaultEnrollTimeout},
		"rack.enrollment_interval":      {func(c *config.RackConfig, v string) { c.Rack.EnrollmentInterval = v }, defaultEnrollInterval},
		"rack.heartbeat_interval":       {func(c *config.RackConfig, v string) { c.Rack.HeartbeatInterval = v }, defaultHeartbeatInterval},
		"snake.inactive_threshold":      {func(c *config.RackConfig, v string) { c.Snake.InactiveThreshold = v }, defaultInactiveThreshold},
	}
	for setting, tc := range cases {
		for _, bad := range []string{"0s", "-1s"} {
			t.Run(setting+"/"+bad, func(t *testing.T) {
				cfg := validTimeouts()
				tc.mutate(cfg, bad)

				got, err := parseRackTimeouts(cfg)

				require.NoError(t, err)
				fields := map[string]time.Duration{
					"rack.cleanup_timeout":          got.cleanup,
					"rack.drain_timeout":            got.drain,
					"rack.convergence_timeout":      got.convergence,
					"rack.handshake_interval":       got.handshake,
					"snake.subscription_retry_wait": got.subscriptionRetry,
					"snake.operation_timeout":       got.operation,
					"snake.connect_timeout":         got.connect,
					"snake.reconnect_wait":          got.reconnectWait,
					"rack.enrollment_timeout":       got.enrollTimeout,
					"rack.enrollment_interval":      got.enrollInterval,
					"rack.heartbeat_interval":       got.heartbeat,
					"snake.inactive_threshold":      got.inactiveThreshold,
				}
				assert.Equal(t, tc.want, fields[setting], "%s did not clamp %q to its documented default", setting, bad)
			})
		}
	}
}

// Each setting that is not a duration stops the start and is named in the error. A
// typo in any of these used to be silently read as zero instead: the initial
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
		"snake.connect_timeout":         func(c *config.RackConfig) { c.Snake.ConnectTimeout = "soon" },
		"snake.reconnect_wait":          func(c *config.RackConfig) { c.Snake.ReconnectWait = "soon" },
		"rack.enrollment_timeout":       func(c *config.RackConfig) { c.Rack.EnrollmentTimeout = "soon" },
		"rack.enrollment_interval":      func(c *config.RackConfig) { c.Rack.EnrollmentInterval = "soon" },
		"rack.heartbeat_interval":       func(c *config.RackConfig) { c.Rack.HeartbeatInterval = "soon" },
		"snake.inactive_threshold":      func(c *config.RackConfig) { c.Snake.InactiveThreshold = "soon" },
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
