// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixercmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestRunHelpExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		if code := Run([]string{flag}); code != 0 {
			t.Errorf("Run(%q) = %d, want 0", flag, code)
		}
	}
}

// A configuration file the operator named and that is malformed must stop the
// Mixer with a failure. Starting on defaults would hide the mistake.
func TestRunFailsOnAnExplicitConfigThatCannotLoad(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "fluxrig-mixer.toml")
	if err := os.WriteFile(bad, []byte("this is = = not toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"--config", bad}); code != 1 {
		t.Fatalf("Run with an unreadable explicit config = %d, want 1", code)
	}
}

// --repair on a fresh store diagnoses absent streams and creates them,
// exiting 0. A second run finds everything healthy and changes nothing.
func TestRunRepairCreatesAbsentStreams(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Repair never mints an identity: the cluster key must already exist.
	ck, err := pki.GenerateClusterKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := ck.Save(filepath.Join(dataDir, "cluster.key")); err != nil {
		t.Fatal(err)
	}

	// The config points at an absolute store dir; relative would resolve to
	// the test's working directory. Write it with the real path.
	cfgPath := filepath.Join(dir, "fluxrig-mixer.toml")
	cfg := "[logging]\nlevel = \"warn\"\n[api]\nport = 18091\n[snake]\nport = 14223\nurl = \"nats://localhost:14223\"\n[store]\ndir = \"" + dataDir + "\"\n[enrollment]\nauto_adopt = true\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"--config", cfgPath, "--repair"}); code != 0 {
		t.Fatalf("first --repair = %d, want 0", code)
	}
	if code := Run([]string{"--config", cfgPath, "--repair"}); code != 0 {
		t.Fatalf("second --repair = %d, want 0 (everything healthy)", code)
	}
}

// --repair with no cluster key fails instead of minting one mid-repair.
func TestRunRepairRefusesWithoutClusterKey(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "fluxrig-mixer.toml")
	cfg := "[logging]\nlevel = \"warn\"\n[api]\nport = 18092\n[snake]\nport = 14224\nurl = \"nats://localhost:14224\"\n[store]\ndir = \"" + dataDir + "\"\n[enrollment]\nauto_adopt = true\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"--config", cfgPath, "--repair"}); code == 0 {
		t.Fatal("--repair without a cluster key must fail, not mint one")
	}
}
