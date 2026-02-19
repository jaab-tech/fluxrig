// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package rotator

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rotator manages log file rotation based on size or time.
type Rotator struct {
	mu         sync.Mutex
	filename   string
	maxSize    int64 // bytes
	maxBackups int
	compress   bool
	file       *os.File
	size       int64
}

// New creates a new Rotator.
// filename: Absolute absolute path to the log file.
// maxSizeMB: Max size in Megabytes before rotation.
// maxBackups: Number of old files to keep.
// compress: Compress rotated files.
func New(filename string, maxSizeMB int, maxBackups int, compress bool) (*Rotator, error) {
	// Ensure directory exists
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create log dir: %w", err)
	}

	r := &Rotator{
		filename:   filename,
		maxSize:    int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
		compress:   compress,
	}

	// Open existing or create new
	if err := r.open(); err != nil {
		return nil, err
	}

	return r, nil
}

// open opens the log file and gets its size.
func (r *Rotator) open() error {
	f, err := os.OpenFile(r.filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	r.file = f

	info, err := f.Stat()
	if err != nil {
		return err
	}
	r.size = info.Size()
	return nil
}

// Write implements io.Writer. It checks for rotation before writing.
func (r *Rotator) Write(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	writeLen := int64(len(p))

	if r.maxSize > 0 && r.size+writeLen > r.maxSize {
		if errRot := r.rotate(); errRot != nil {
			return 0, errRot
		}
	}

	n, err = r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the file.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}

// rotate closes the current file, renames it, and opens a new one.
// It matches the standard scheme: filename.timestamp
// Or filename.1, filename.2 (Logrotate style)?
// For traceability, timestamp is better: filename.2023-10-27T10-00-00.000
// But simpler implementation often uses .1 shift.
// Requirement: "Log Lifecycle".
// I'll use Timestamp suffix for uniqueness and ease of sorting.
func (r *Rotator) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}

	// Rename
	timestamp := time.Now().Format("2006-01-02T15-04-05.000")
	backupName := fmt.Sprintf("%s.%s", r.filename, timestamp)

	if err := os.Rename(r.filename, backupName); err != nil {
		return fmt.Errorf("failed to rename log file: %w", err)
	}

	// Open new
	if err := r.open(); err != nil {
		return fmt.Errorf("failed to open new log file: %w", err)
	}

	// Async cleanup & compression
	go r.cleanup(backupName)

	return nil
}

func (r *Rotator) cleanup(newBackup string) {
	// 1. Compress the new backup if requested
	if r.compress {
		if err := compressFile(newBackup); err != nil {
			// Log error to... stderr? We are the logger.
			// Just ignore for now or print to stderr
			fmt.Fprintf(os.Stderr, "Rotator: failed to compress %s: %v\n", newBackup, err)
		} else {
			// Remove original if compressed successfully
			_ = os.Remove(newBackup)
		}
	}

	// 2. Prune old backups
	if r.maxBackups == 0 {
		return
	}

	// Find matches
	dir := filepath.Dir(r.filename)
	base := filepath.Base(r.filename)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var backups []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Match prefix: "rack.wal." or "rack.log."
		if strings.HasPrefix(name, base+".") {
			// Exclude active file (already excluded by ".")
			// Exclude .gz? No, include them.
			backups = append(backups, filepath.Join(dir, name))
		}
	}

	// Sort (Alpha string sort of timestamps works ISO8601)
	sort.Strings(backups)

	// Delete oldest
	// strings are sorted Oldest -> Newest (lexicographical)
	// We want to keep LAST maxBackups
	if len(backups) <= r.maxBackups {
		return
	}

	toDelete := len(backups) - r.maxBackups
	for i := 0; i < toDelete; i++ {
		_ = os.Remove(backups[i])
	}
}

func compressFile(src string) error {
	f, err := os.Open(filepath.Clean(src))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	dst := src + ".gz"
	out, err := os.Create(filepath.Clean(dst))
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	w := gzip.NewWriter(out)
	defer func() { _ = w.Close() }()

	if _, err := io.Copy(w, f); err != nil {
		return err
	}

	return nil
}
