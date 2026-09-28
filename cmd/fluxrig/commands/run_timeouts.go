// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

// rackTimeouts are the durations a Rack session reads from its configuration and
// refuses to start without.
type rackTimeouts struct {
	cleanup           time.Duration
	drain             time.Duration
	convergence       time.Duration
	handshake         time.Duration
	subscriptionRetry time.Duration
	operation         time.Duration
	initialRetryWait  time.Duration
	offlineStart      time.Duration
	offlineRetry      time.Duration
	laneSend          time.Duration
	connect           time.Duration
	reconnectWait     time.Duration
	enrollTimeout     time.Duration
	enrollInterval    time.Duration
	heartbeat         time.Duration
	inactiveThreshold time.Duration
}

// The documented defaults from pkg/config's LoadRack, repeated here so a
// non-positive setting has something sane to fall back to instead of the zero
// value it parsed to. Keep these in sync with LoadRack's own k.Set calls: they
// name the same keys and must carry the same values, or a Rack that never sets
// one of these explicitly gets a different effective default than the one the
// documentation and LoadRack agree on.
const (
	defaultCleanupTimeout     = 2 * time.Second
	defaultDrainTimeout       = 35 * time.Second
	defaultConvergenceTimeout = 5 * time.Second
	defaultHandshakeInterval  = 500 * time.Millisecond
	defaultSubscriptionRetry  = 200 * time.Millisecond
	defaultOperationTimeout   = 5 * time.Second
	defaultConnectTimeout     = 10 * time.Second
	defaultReconnectWait      = 1 * time.Second
	defaultEnrollTimeout      = 15 * time.Second
	defaultEnrollInterval     = 2 * time.Second
	defaultHeartbeatInterval  = 30 * time.Second
	defaultInactiveThreshold  = 30 * time.Second
)

// parseRackTimeouts reads them, and names the setting that is not a duration.
func parseRackTimeouts(cfg *config.RackConfig) (rackTimeouts, error) {
	var (
		out rackTimeouts
		err error
	)
	if out.cleanup, err = parseTimeout("rack.cleanup_timeout", cfg.Rack.CleanupTimeout, defaultCleanupTimeout); err != nil {
		return out, err
	}
	if out.drain, err = parseTimeout("rack.drain_timeout", cfg.Rack.DrainTimeout, defaultDrainTimeout); err != nil {
		return out, err
	}
	if out.convergence, err = parseTimeout("rack.convergence_timeout", cfg.Rack.ConvergenceTimeout, defaultConvergenceTimeout); err != nil {
		return out, err
	}
	// A non-positive handshake interval would reach time.NewTicker in
	// telemetry.VerifyConnectivity and panic; clamped like every other
	// interval below that ends up as a ticker's period.
	if out.handshake, err = parseTimeout("rack.handshake_interval", cfg.Rack.HandshakeInterval, defaultHandshakeInterval); err != nil {
		return out, err
	}
	if out.subscriptionRetry, err = parseTimeout("snake.subscription_retry_wait", cfg.Snake.SubscriptionRetryWait, defaultSubscriptionRetry); err != nil {
		return out, err
	}
	// The bound on every gear Process call (runtime.go's wire handler): a
	// non-positive value here does not just misbehave once at startup, it
	// times out every single message the Rack processes.
	if out.operation, err = parseTimeout("snake.operation_timeout", cfg.Snake.OperationTimeout, defaultOperationTimeout); err != nil {
		return out, err
	}

	// "0s" is not a typo for these four: it is the documented sentinel that
	// turns off the initial-retry bound, the offline probe or the lane send
	// bound, and each of those call sites already treats zero (or less) as
	// "use my own built-in default" or "disable this". Clamping here would
	// silence that switch instead of respecting it, so these four keep the
	// plain parse with no floor.
	if out.initialRetryWait, err = time.ParseDuration(cfg.Snake.InitialRetryWait); err != nil {
		return out, fmt.Errorf("invalid snake.initial_retry_wait: %w", err)
	}
	if out.offlineStart, err = time.ParseDuration(cfg.Snake.OfflineStartTimeout); err != nil {
		return out, fmt.Errorf("invalid snake.offline_start_timeout: %w", err)
	}
	if out.offlineRetry, err = time.ParseDuration(cfg.Snake.OfflineRetryInterval); err != nil {
		return out, fmt.Errorf("invalid snake.offline_retry_interval: %w", err)
	}
	if out.laneSend, err = time.ParseDuration(cfg.Rack.LaneSendTimeout); err != nil {
		return out, fmt.Errorf("invalid rack.lane_send_timeout: %w", err)
	}

	// These six used to be parsed ad hoc in runSession with the parse error
	// discarded outright, so a typo silently read as zero: connectTimeout=0
	// blocked the initial NATS dial forever instead of bounding it,
	// enrollInterval=0 and heartbeat=0 would panic their time.NewTicker, and
	// enrollTimeout=0 timed out enrollment on the first tick.
	if out.connect, err = parseTimeout("snake.connect_timeout", cfg.Snake.ConnectTimeout, defaultConnectTimeout); err != nil {
		return out, err
	}
	if out.reconnectWait, err = parseTimeout("snake.reconnect_wait", cfg.Snake.ReconnectWait, defaultReconnectWait); err != nil {
		return out, err
	}
	if out.enrollTimeout, err = parseTimeout("rack.enrollment_timeout", cfg.Rack.EnrollmentTimeout, defaultEnrollTimeout); err != nil {
		return out, err
	}
	if out.enrollInterval, err = parseTimeout("rack.enrollment_interval", cfg.Rack.EnrollmentInterval, defaultEnrollInterval); err != nil {
		return out, err
	}
	if out.heartbeat, err = parseTimeout("rack.heartbeat_interval", cfg.Rack.HeartbeatInterval, defaultHeartbeatInterval); err != nil {
		return out, err
	}
	// Same clamp runSession already applied by hand for exactly this one
	// setting (== 0 only); centralized here and extended to negative values.
	if out.inactiveThreshold, err = parseTimeout("snake.inactive_threshold", cfg.Snake.InactiveThreshold, defaultInactiveThreshold); err != nil {
		return out, err
	}

	return out, nil
}

// parseTimeout parses raw as a duration, naming field in the error when it is not
// one. A value that parses but is not positive is replaced by def: left alone, a
// "0s" typo would fire whatever it bounds (a ticker, a per-message timeout, a
// connect deadline) instantly, or panic a ticker outright, rather than fail loudly
// at config-load time or do nothing.
func parseTimeout(field, raw string, def time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", field, err)
	}
	if d <= 0 {
		return def, nil
	}
	return d, nil
}
