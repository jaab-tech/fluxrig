// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"path/filepath"
	"testing"

	"log/slog"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/config"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

func TestRunAgent_Failures(t *testing.T) {
	// 1. Bad Data Dir -> Should Fail
	cfg := &config.RackConfig{}
	cfg.Store.Dir = "/invalid/path/that/cannot/be/created/root/protection"
	// Assuming permissions fail or nested too deep/readonly.
	// Or simpler: file exists as directory name.

	tmpDir := t.TempDir()
	conflict := filepath.Join(tmpDir, "file_blocking_dir")
	f, _ := os.Create(filepath.Clean(conflict))
	_ = f.Close()

	cfg.Store.Dir = conflict

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	buf := telemetry.NewBufferHandler(logger.Handler())

	err := RunAgent(cfg, logger, buf)
	if err == nil {
		t.Error("Expected error for bad data dir")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestInspectConfig(t *testing.T) {
	// Not testing Cobra machinery, but logic if extracted.
	// Logic is inside RunE closure. Not easily accessible.
	// Skipping.
}

func TestSetupLogger(t *testing.T) {
	cfg := &config.RackConfig{}
	cfg.Logging.Level = "debug"

	l := setupLogger(cfg)
	if l == nil {
		t.Error("Logger is nil")
	}
	if !l.Enabled(context.TODO(), slog.LevelDebug) {
		t.Error("Logger should be debug")
	}

	// Config Override
	cfg.Logging.Trace = true
	l2 := setupLogger(cfg)
	// Trace level check
	if !l2.Enabled(context.TODO(), loggerPkg.LevelTrace) {
		t.Error("Logger should be trace")
	}
}
