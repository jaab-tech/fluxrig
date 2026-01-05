package commands

import (
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/config"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"log/slog"
	"os"
)

func TestRunAgent_Failures(t *testing.T) {
	// 1. Bad Data Dir -> Should Fail
	cfg := &config.RackConfig{}
	cfg.Store.Dir = "/invalid/path/that/cannot/be/created/root/protection"
	// Assuming permissions fail or nested too deep/readonly.
	// Or simpler: file exists as directory name.

	tmpDir := t.TempDir()
	conflict := filepath.Join(tmpDir, "file_blocking_dir")
	os.Create(conflict)

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
	if !l.Enabled(nil, slog.LevelDebug) {
		t.Error("Logger should be debug")
	}

	// Env Override
	os.Setenv("FLUXRIG_TRACE", "true")
	defer os.Unsetenv("FLUXRIG_TRACE")
	l2 := setupLogger(cfg)
	// Trace level check (not standardized in slog until recently/custom, assuming loggerPkg supports it)
	if !l2.Enabled(nil, loggerPkg.LevelTrace) {
		t.Error("Logger should be trace")
	}
}
