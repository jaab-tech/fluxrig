// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRetentionDays = 30
	janitorInterval      = 1 * time.Hour
)

// Janitor manages telemetry data retention by cleaning up old files.
type Janitor struct {
	logger        *slog.Logger
	dataDir       string
	retentionDays int
}

// NewJanitor creates a new retention manager.
func NewJanitor(logger *slog.Logger, dataDir string, retentionDays int) *Janitor {
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	return &Janitor{
		logger:        logger,
		dataDir:       dataDir,
		retentionDays: retentionDays,
	}
}

// Start begins the janitor loop in a goroutine.
func (j *Janitor) Start(ctx context.Context) {
	// Run once immediately on startup
	if err := j.clean(); err != nil {
		j.logger.Error("janitor failed on startup", "error", err)
	}

	ticker := time.NewTicker(janitorInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := j.clean(); err != nil {
					j.logger.Error("janitor failed", "error", err)
				}
			}
		}
	}()
}

// clean scans the data directory and deletes files older than retention policy.
func (j *Janitor) clean() error {
	cutoff := time.Now().AddDate(0, 0, -j.retentionDays)
	j.logger.Info("janitor starting cleanup",
		"retention_days", j.retentionDays,
		"cutoff", cutoff)

	var deletedCount int
	var reclaimedBytes int64

	// Ensure directory exists to avoid walk error on fresh installations
	if _, err := os.Stat(j.dataDir); os.IsNotExist(err) {
		return nil
	}

	err := filepath.Walk(j.dataDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		// Only target .parquet files in telemetry directories
		if !strings.HasSuffix(info.Name(), ".parquet") {
			return nil
		}

		// Parse timestamp from filename to be accurate, fallback to ModTime
		// Format: type_TIMESTAMP.parquet
		// e.g. metrics_1767735924810.parquet
		ts := info.ModTime()
		parts := strings.Split(strings.TrimSuffix(info.Name(), ".parquet"), "_")
		if len(parts) >= 2 {
			if nanos, err := strconv.ParseInt(parts[len(parts)-1], 10, 64); err == nil {
				ts = time.Unix(0, nanos)
			}
		}

		if ts.Before(cutoff) {
			size := info.Size()
			if err := os.Remove(path); err != nil {
				j.logger.Warn("failed to delete expired file", "path", path, "error", err)
				return nil // Continue scanning
			}
			deletedCount++
			reclaimedBytes += size
		}
		return nil
	})

	if deletedCount > 0 {
		j.logger.Info("janitor cleanup complete",
			"files_deleted", deletedCount,
			"bytes_reclaimed", reclaimedBytes)
	}

	return err
}
