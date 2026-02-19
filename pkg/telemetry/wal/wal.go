// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"fmt"
	"os"
	"sync"

	"github.com/tidwall/wal"
	"github.com/vmihailenco/msgpack/v5"
)

var (
	ErrNotFound = wal.ErrNotFound
)

// Options aliases tidwall/wal.Options
type Options = wal.Options

// WAL wraps tidwall/wal with MsgPack encoding and auto-indexing.
type WAL struct {
	mu  sync.Mutex
	dir string
	log *wal.Log
}

// Open opens a WAL at the specified directory.
func Open(dir string, opts *wal.Options) (*WAL, error) {
	l, err := wal.Open(dir, opts)
	if err != nil {
		return nil, err
	}
	return &WAL{log: l, dir: dir}, nil
}

// Write appends a value to the log.
// It auto-increments the index based on LastIndex + 1.
func (w *WAL) Write(v interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. Marshal payload
	data, err := msgpack.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	// 2. Determine Next Index
	lastIdx, err := w.log.LastIndex()
	if err != nil {
		return fmt.Errorf("failed to get last index: %w", err)
	}
	nextIdx := lastIdx + 1

	// 3. Write
	if err := w.log.Write(nextIdx, data); err != nil {
		return fmt.Errorf("wal write failed: %w", err)
	}
	return nil
}

// Read reads the record at the given index.
func (w *WAL) Read(index uint64) ([]byte, error) {
	return w.log.Read(index)
}

// TruncateFront deletes all records before the given index.
func (w *WAL) TruncateFront(index uint64) error {
	return w.log.TruncateFront(index)
}

// FirstIndex returns the first available index.
func (w *WAL) FirstIndex() (uint64, error) {
	return w.log.FirstIndex()
}

// LastIndex returns the last written index.
func (w *WAL) LastIndex() (uint64, error) {
	return w.log.LastIndex()
}

// Close closes the log.
func (w *WAL) Close() error {
	return w.log.Close()
}

// Size returns the approximate size of the WAL directory in bytes.
// It iterates files in the directory.
func (w *WAL) Size() (int64, error) {
	// tidwall/wal doesn't expose size directly.
	// We sum up file sizes in the directory.
	// This is expensive if called too often, so caller should rate limit.
	var size int64
	files, err := os.ReadDir(w.dir)
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			continue
		}
		if !f.IsDir() {
			size += info.Size()
		}
	}
	return size, nil
}
