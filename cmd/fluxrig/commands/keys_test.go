// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/spf13/cobra"
)

// params: args to pass to command
// returns: stdout, error
func executeCommand(root *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	// We need to capture os.Stdout too because some fmt.Printf calls might bypass cobra's Out
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := root.Execute()

	_ = w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	return string(out) + buf.String(), err
}

func TestKeysGenCluster(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "mycluster.key")

	// 1. Run gen-cluster
	out, err := executeCommand(rootCmd, "keys", "gen-cluster", "-o", keyPath)
	if err != nil {
		t.Fatalf("Command failed: %v", err)
	}

	// 2. Verify Output
	if !strings.Contains(out, "Cluster Private Key saved") {
		t.Errorf("Unexpected output: %s", out)
	}

	// 3. Verify File
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("Key file was not created")
	}

	// 4. Verify Content
	if _, err := pki.LoadClusterKey(keyPath); err != nil {
		t.Errorf("Failed to load generated key: %v", err)
	}
}

func TestKeysInspect(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Generate Authority
	tmpKey := filepath.Join(tmpDir, "root.key")
	if _, err := executeCommand(rootCmd, "keys", "gen-cluster", "-o", tmpKey); err != nil {
		t.Fatal(err)
	}
	ck, _ := pki.LoadClusterKey(tmpKey)

	// 2. Create Signed Passport
	state := &pki.RackState{
		MachineID:   101,
		Name:        "test-rack",
		Status:      "active",
		Secret:      "ABCDEF",
		MixerPublic: ck.Public,
	}
	env, err := ck.Sign(state)
	if err != nil {
		t.Fatal(err)
	}

	passportPath := filepath.Join(tmpDir, "state.flux")
	if errSave := env.Save(passportPath); errSave != nil {
		t.Fatal(errSave)
	}

	// 3. Run Inspect
	out, err := executeCommand(rootCmd, "keys", "inspect", passportPath)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}

	// 4. Verify Output
	if !strings.Contains(out, "Signature Verification Passed") {
		t.Error("Verification failed output")
	}
	if !strings.Contains(out, "MachineID: 101") {
		t.Error("Identity not displayed")
	}
	if !strings.Contains(out, "Name:      test-rack") {
		t.Error("Name not displayed")
	}
}
