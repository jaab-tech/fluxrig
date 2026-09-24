// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
)

// watchForBus probes the bus every interval until it answers, then signals
// restart once and returns. The session that follows joins the Mixer through the
// ordinary online path (hello, passport, telemetry, scenario), and takes over the
// gears the Rack is running, if any, instead of stopping them.
//
// Each probe is a short-lived connection of its own, made with the Rack's own
// bus options and a single attempt, so a bus that is down never holds the watcher
// beyond the connect timeout.
func watchForBus(ctx context.Context, url string, opts bus.ConnectOptions, stream string, interval time.Duration, restart chan<- struct{}, logger *slog.Logger) {
	probeOpts := opts
	probeOpts.Name = opts.Name + "-probe"
	probeOpts.InitialRetryAttempts = 1
	probeOpts.InitialRetryTimeout = 0

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		probe := bus.NewNatsBus(stream)
		if err := probe.Connect(url, probeOpts); err != nil {
			logger.Debug("Bus still unreachable", "error", err)
			continue
		}
		probe.Close()

		if ctx.Err() != nil {
			return
		}
		logger.Info("Bus reachable again. Joining the Mixer; the gears that are running stay.", "url", url)
		select {
		case restart <- struct{}{}:
		default:
		}
		return
	}
}

// offlineStartBound is how long the first bus connection may be retried. A Rack
// holding a passport can run without the Mixer, so it gives the bus only that
// window before starting offline. One without a passport has nothing to run on and
// keeps the full retry schedule, which is a bound of zero.
func offlineStartBound(machineID uuid.UUID, timeout time.Duration) time.Duration {
	if machineID == uuid.Nil {
		return 0
	}
	return timeout
}

// startBusWatcher runs watchForBus in the background for a Rack that started
// offline, and returns the function that stops it. With no interval configured it
// starts nothing.
func startBusWatcher(ctx context.Context, url string, opts bus.ConnectOptions, stream string, interval time.Duration, restart chan<- struct{}, logger *slog.Logger) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	go watchForBus(watchCtx, url, opts, stream, interval, restart, logger)
	return cancel
}
