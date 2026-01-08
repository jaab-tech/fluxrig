// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shipper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// CursorState represents the persisted state of the log shipper.
type CursorState struct {
	Offset uint64 `json:"offset"`
}

// Cursor manages the read offset persistence.
type Cursor struct {
	mu    sync.Mutex
	path  string
	State CursorState
}

// NewCursor loads or creates a cursor at the given path.
func NewCursor(path string) (*Cursor, error) {
	c := &Cursor{path: path}

	f, err := os.Open(filepath.Clean(path))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if err := json.NewDecoder(f).Decode(&c.State); err != nil {
		return c, nil
	}

	return c, nil
}

// Save persists the current state to disk.
func (c *Cursor) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Atomic write via temp file
	tmpPath := c.path + ".tmp"
	f, err := os.Create(filepath.Clean(tmpPath))
	if err != nil {
		return err
	}

	if err := json.NewEncoder(f).Encode(c.State); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, c.path)
}

// Update updates the in-memory offset.
func (c *Cursor) Update(offset uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State.Offset = offset
}

// Get returns the current offset.
func (c *Cursor) Get() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.State.Offset
}
