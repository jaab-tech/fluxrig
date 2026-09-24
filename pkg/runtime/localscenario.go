// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// ErrLocalScenarioMismatch reports a saved scenario that was projected for
// another Rack: a different name or machine ID than the one asking for it.
var ErrLocalScenarioMismatch = errors.New("saved scenario belongs to another rack")

// LocalScenarioStore keeps a Rack's own copy of the last scenario the Mixer sent
// it and it applied, so the Rack can resume that scenario without waiting for
// the Mixer to send it again.
//
// The copy is the payload as the Mixer sent it, minus the spec artefacts: those
// are filed in the Rack's spec store when the payload arrives, and that store is
// what a gear resolves them against.
type LocalScenarioStore struct {
	path string
}

// NewLocalScenarioStore returns a store that keeps its copy at path.
func NewLocalScenarioStore(path string) *LocalScenarioStore {
	return &LocalScenarioStore{path: path}
}

// Path is where the copy is kept.
func (s *LocalScenarioStore) Path() string { return s.path }

// Save replaces the saved copy with p. The write is atomic: a crash leaves the
// previous copy or the new one, never half of either.
func (s *LocalScenarioStore) Save(p *fluxmsg.ScenarioPayload) error {
	if p == nil || len(p.Scenario) == 0 {
		return errors.New("local scenario: nothing to save")
	}
	kept := *p
	kept.Specs = nil

	data, err := cbor.Marshal(&kept)
	if err != nil {
		return fmt.Errorf("local scenario: encode: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("local scenario: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("local scenario: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err = os.Chmod(tmpName, 0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("local scenario: set permissions: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("local scenario: write: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("local scenario: sync: %w", err)
	}
	if err = tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("local scenario: close: %w", err)
	}
	if err = os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return fmt.Errorf("local scenario: replace %s: %w", s.path, err)
	}
	return nil
}

// Load returns the saved copy for the Rack with this name and machine ID.
//
// It returns (nil, nil) when nothing has been saved, an error wrapping
// ErrLocalScenarioMismatch when the copy was projected for another Rack, and any
// other error when the file cannot be read or decoded.
func (s *LocalScenarioStore) Load(machineID uuid.UUID, rackName string) (*fluxmsg.ScenarioPayload, error) {
	data, err := os.ReadFile(filepath.Clean(s.path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("local scenario: read %s: %w", s.path, err)
	}

	var p fluxmsg.ScenarioPayload
	if err := cbor.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("local scenario: decode %s: %w", s.path, err)
	}
	if len(p.Scenario) == 0 {
		return nil, fmt.Errorf("local scenario: %s holds no scenario", s.path)
	}
	if p.MachineID != machineID || p.RackName != rackName {
		return nil, fmt.Errorf("%w: saved for %q (%s), this rack is %q (%s)",
			ErrLocalScenarioMismatch, p.RackName, p.MachineID, rackName, machineID)
	}
	return &p, nil
}
