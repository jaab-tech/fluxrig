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
}

// parseRackTimeouts reads them, and names the setting that is not a duration.
func parseRackTimeouts(cfg *config.RackConfig) (rackTimeouts, error) {
	var (
		out rackTimeouts
		err error
	)
	if out.cleanup, err = time.ParseDuration(cfg.Rack.CleanupTimeout); err != nil {
		return out, fmt.Errorf("invalid rack.cleanup_timeout: %w", err)
	}
	if out.drain, err = time.ParseDuration(cfg.Rack.DrainTimeout); err != nil {
		return out, fmt.Errorf("invalid rack.drain_timeout: %w", err)
	}
	if out.convergence, err = time.ParseDuration(cfg.Rack.ConvergenceTimeout); err != nil {
		return out, fmt.Errorf("invalid rack.convergence_timeout: %w", err)
	}
	if out.handshake, err = time.ParseDuration(cfg.Rack.HandshakeInterval); err != nil {
		return out, fmt.Errorf("invalid rack.handshake_interval: %w", err)
	}
	if out.subscriptionRetry, err = time.ParseDuration(cfg.Snake.SubscriptionRetryWait); err != nil {
		return out, fmt.Errorf("invalid snake.subscription_retry_wait: %w", err)
	}
	if out.operation, err = time.ParseDuration(cfg.Snake.OperationTimeout); err != nil {
		return out, fmt.Errorf("invalid snake.operation_timeout: %w", err)
	}
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
	return out, nil
}
