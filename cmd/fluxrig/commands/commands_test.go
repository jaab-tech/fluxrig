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

package commands

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	// Capture output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Set args to "version"
	rootCmd.SetArgs([]string{"version"})
	if err := os.Setenv("FLUXRIG_HOME", "/tmp"); err != nil {
		t.Fatal(err)
	}

	// Execute
	// Execute
	if err := Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Restore
	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "fluxrig") {
		t.Errorf("Expected output to contain 'fluxrig', got: %s", output)
	}
}
