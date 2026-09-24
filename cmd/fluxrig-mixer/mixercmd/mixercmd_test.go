// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixercmd

import (
	"os"
	"path/filepath"
	"testing"
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
