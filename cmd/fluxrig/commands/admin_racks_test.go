// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// executeCommandWithStdin is executeCommand, but with stdin fed from input
// instead of the test process's real (usually unattached) stdin, so a
// confirmation prompt can be answered deterministically.
func executeCommandWithStdin(root *cobra.Command, input string, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	root.SetIn(strings.NewReader(input))
	defer root.SetIn(nil)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := root.Execute()

	_ = w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	return string(out) + buf.String(), err
}

func TestRacksList(t *testing.T) {
	// Mock Server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/racks" {
			racks := []map[string]any{
				{"machine_id": uuid.New().String(), "name": "rack-1", "status": "active"},
			}
			_ = json.NewEncoder(w).Encode(racks)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	// Execute Command
	out, err := executeCommand(rootCmd, "admin", "racks", "list", "--api-url", mockServer.URL)
	if err != nil {
		t.Fatalf("Command failed: %v", err)
	}

	// Verify Output
	if !strings.Contains(out, "rack-1") {
		t.Errorf("Expected 'rack-1' in output, got: %s", out)
	}
}

func TestRacksApprove(t *testing.T) {
	id := uuid.New().String()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/approve") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	out, err := executeCommand(rootCmd, "admin", "racks", "approve", id, "--name", "new-name", "--api-url", mockServer.URL)
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	if !strings.Contains(out, "approved") {
		t.Errorf("Expected 'approved' in output, got: %s", out)
	}
}

func TestRacksSuspend(t *testing.T) {
	id := uuid.New().String()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/suspend") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	out, err := executeCommand(rootCmd, "admin", "racks", "suspend", id, "--api-url", mockServer.URL, "--force")
	if err != nil {
		t.Fatalf("Suspend failed: %v", err)
	}

	if !strings.Contains(out, "suspend") {
		t.Errorf("Expected 'suspend' in output, got: %s", out)
	}
}

func TestRacksActivate(t *testing.T) {
	id := uuid.New().String()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/activate") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	out, err := executeCommand(rootCmd, "admin", "racks", "activate", id, "--api-url", mockServer.URL, "--force")
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	if !strings.Contains(out, "activated") {
		t.Errorf("Expected 'activated' in output, got: %s", out)
	}
}

// TestAdminRacksSendsBearerToken confirms every admin racks call actually
// attaches the configured API token: the Mixer's management API rejects
// every route but /health with no token, so a CLI that built the right
// request but never sent it would fail against a real, correctly configured
// Mixer while still passing a mock that does not check auth at all.
func TestAdminRacksSendsBearerToken(t *testing.T) {
	const wantToken = "test-admin-token"

	requireAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/racks":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer requireAuth.Close()

	t.Run("with --api-token", func(t *testing.T) {
		_, err := executeCommand(rootCmd, "admin", "racks", "list", "--api-url", requireAuth.URL, "--api-token", wantToken)
		if err != nil {
			t.Fatalf("expected success with the correct token, got: %v", err)
		}
	})

	t.Run("with FLUXRIG_API_TOKEN", func(t *testing.T) {
		t.Setenv("FLUXRIG_API_TOKEN", wantToken)
		_, err := executeCommand(rootCmd, "admin", "racks", "list", "--api-url", requireAuth.URL)
		if err != nil {
			t.Fatalf("expected success via the env var, got: %v", err)
		}
	})

	t.Run("no token at all is rejected", func(t *testing.T) {
		_, err := executeCommand(rootCmd, "admin", "racks", "list", "--api-url", requireAuth.URL, "--api-token", "")
		if err == nil {
			t.Fatal("expected the request with no token to fail against an auth-requiring Mixer")
		}
	})
}

func TestRacksRemove(t *testing.T) {
	id := uuid.New().String()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	out, err := executeCommand(rootCmd, "admin", "racks", "remove", id, "--api-url", mockServer.URL, "--force")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if !strings.Contains(out, "removed") {
		t.Errorf("Expected 'removed' in output, got: %s", out)
	}
}

func TestRacksShutdown(t *testing.T) {
	id := uuid.New().String()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/shutdown") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	out, err := executeCommand(rootCmd, "admin", "racks", "shutdown", id, "--api-url", mockServer.URL, "--force")
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if !strings.Contains(out, "shutdown") {
		t.Errorf("Expected 'shutdown' in output, got: %s", out)
	}
}

// Before this fix, remove/suspend/activate/shutdown ran unconditionally: no
// prompt, no --force flag existed to skip one. These four cases cover the
// mutating commands' whole confirmation contract: a bare run (no terminal
// attached, no --force) must abort rather than hang or silently proceed;
// an explicit "n" must abort too; an explicit "y" must proceed; and --force
// must skip the prompt entirely with no stdin needed at all.
func TestRacksDestructiveCommands_RequireConfirmationOrForce(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	tests := []struct {
		name    string
		verb    string
		stdin   string
		force   bool
		wantErr bool
	}{
		{"no terminal, no force: aborts", "suspend", "", false, true},
		{"explicit no: aborts", "suspend", "n\n", false, true},
		{"explicit yes: proceeds", "suspend", "y\n", false, false},
		{"force: proceeds without any stdin", "suspend", "", true, false},
		{"activate, explicit yes: proceeds", "activate", "y\n", false, false},
		{"remove, explicit no: aborts", "remove", "n\n", false, true},
		{"shutdown, force: proceeds", "shutdown", "", true, false},
	}

	verbCmds := map[string]*cobra.Command{
		"suspend":  racksSuspendCmd,
		"activate": racksActivateCmd,
		"remove":   racksRemoveCmd,
		"shutdown": racksShutdownCmd,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// cobra flags live on the (package-level, shared-across-tests)
			// *cobra.Command and do not reset between Execute() calls, so an
			// earlier test's --force would otherwise leak into this one.
			if errReset := verbCmds[tc.verb].Flags().Set("force", "false"); errReset != nil {
				t.Fatalf("failed to reset --force: %v", errReset)
			}

			id := uuid.New().String()
			args := []string{"admin", "racks", tc.verb, id, "--api-url", mockServer.URL}
			if tc.force {
				args = append(args, "--force")
			}
			_, err := executeCommandWithStdin(rootCmd, tc.stdin, args...)
			if tc.wantErr && err == nil {
				t.Error("expected an error (aborted), got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

// The duplicate AddCommand(racksActivateCmd) meant "activate" was
// registered twice under "racks".
func TestRacksActivateCmd_RegisteredOnlyOnce(t *testing.T) {
	count := 0
	for _, c := range adminRacksCmd.Commands() {
		if c == racksActivateCmd {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected racksActivateCmd registered exactly once under racks, found %d", count)
	}
}
