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
	if err := Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Restore
	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if !strings.Contains(output, "fluxrig") {
		t.Errorf("Expected output to contain 'fluxrig', got: %s", output)
	}
}
