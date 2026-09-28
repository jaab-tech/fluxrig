// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

// Before this fix, the two telemetry dial sites in runSession threaded
// RootCA through but not InsecureSkipVerify, unlike the main bus dial a few
// lines above them. An operator relying on insecure_skip_verify (self-signed
// certs, no CA file to distribute) got a Rack whose main connection worked
// and whose telemetry silently failed its TLS handshake instead.
func TestTelemetryConnectOptions_MatchesMainDialTLSPosture(t *testing.T) {
	cfg := &config.RackConfig{}
	cfg.Snake.RootCAFile = "ca.pem"
	cfg.Snake.InsecureSkipVerify = true
	cfg.Snake.Domain = "flux"

	opts := telemetryConnectOptions(cfg, "rack-1", 10*time.Second, time.Second, 30*time.Second, 500*time.Millisecond, 10)

	if opts.RootCA != "ca.pem" {
		t.Errorf("RootCA did not carry through: got %q", opts.RootCA)
	}
	if !opts.InsecureSkipVerify {
		t.Error("InsecureSkipVerify did not carry through: telemetry would attempt real cert verification the main bus dial does not")
	}
	if opts.Name != "rack-1-telemetry" {
		t.Errorf("unexpected client name: got %q", opts.Name)
	}
}
