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
	defaultInterval      = 1 * time.Hour
)

// minPlausibleTimestamp is the earliest a file-name timestamp is accepted as
// real. A name suffix that parses as a number but lands before this is treated
// as absent, and the modification time governs instead.
var minPlausibleTimestamp = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// Janitor manages telemetry data retention by cleaning up old files.
type Janitor struct {
	logger        *slog.Logger
	dataDir       string
	retentionDays int
	interval      time.Duration
}

// NewJanitor creates a new retention manager. A non-positive retentionDays
// falls back to the default. A non-positive interval falls back to one hour.
func NewJanitor(logger *slog.Logger, dataDir string, retentionDays int, interval time.Duration) *Janitor {
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Janitor{
		logger:        logger,
		dataDir:       dataDir,
		retentionDays: retentionDays,
		interval:      interval,
	}
}

// Start begins the janitor loop in a goroutine.
func (j *Janitor) Start(ctx context.Context) {
	// Run once immediately on startup
	if err := j.clean(); err != nil {
		j.logger.Error("janitor failed on startup", "error", err)
	}

	ticker := time.NewTicker(j.interval)
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
		if err != nil {
			// An unreadable entry must not abort the whole sweep.
			j.logger.Warn("failed to inspect path during retention sweep", "path", path, "error", err)
			return nil
		}
		if info == nil || info.IsDir() {
			return nil
		}
		// Only target .parquet files in telemetry directories
		if !strings.HasSuffix(info.Name(), ".parquet") {
			return nil
		}

		// Prefer the timestamp in the file name, because it is the flush time
		// and does not move if the file is later touched. Fall back to the
		// modification time. A name timestamp is trusted only when it is a
		// plausible epoch-nanosecond value: a truncated or malformed suffix
		// would otherwise read as 1970 and delete a file that is still inside
		// the retention window.
		// Format: type_TIMESTAMP.parquet
		// e.g. metrics_1767735924810.parquet
		ts := info.ModTime()
		parts := strings.Split(strings.TrimSuffix(info.Name(), ".parquet"), "_")
		if len(parts) >= 2 {
			if nanos, err := strconv.ParseInt(parts[len(parts)-1], 10, 64); err == nil {
				fromName := time.Unix(0, nanos)
				if !fromName.Before(minPlausibleTimestamp) {
					ts = fromName
				}
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
