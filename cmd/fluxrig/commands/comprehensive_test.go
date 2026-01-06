package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestTelemetryCommands(t *testing.T) {
	// 1. Mock Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1/telemetry/logs" {
			// Return logs
			logs := []map[string]any{
				{
					"timestamp":   time.Now().Format(time.RFC3339),
					"entity_name": "test-entity",
					"severity":    "INFO",
					"body":        "test log",
					"attributes":  map[string]any{},
				},
			}
			_ = json.NewEncoder(w).Encode(logs)
			return
		}
		if path == "/api/v1/telemetry/metrics" {
			metrics := []map[string]any{
				{
					"timestamp":   time.Now().Format(time.RFC3339),
					"entity_name": "test-entity",
					"name":        "cpu",
					"type":        "gauge",
					"value":       50.0,
					"attributes":  map[string]any{},
				},
			}
			_ = json.NewEncoder(w).Encode(metrics)
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer ts.Close()

	// 2. Test logs command
	// Reset flags? Cobra flags persist if global vars.
	// logsCmd is package var.
	// We should be careful about parallel tests.

	// Logs
	logsCmd.SetArgs([]string{
		"--api-url", ts.URL,
		"--limit", "1",
		"--min-level", "INFO",
	})
	// Redirect stdout?
	// The command writes to os.Stdout wrapped in TabWriter.
	// We can't easily capture os.Stdout without pipe tricks.
	// But coverage counts execution.
	if err := logsCmd.Execute(); err != nil {
		t.Errorf("logsCmd failed: %v", err)
	}

	// Metrics
	metricsCmd.SetArgs([]string{
		"--api-url", ts.URL,
		"--limit", "1",
		"--name", "cpu",
	})
	if err := metricsCmd.Execute(); err != nil {
		t.Errorf("metricsCmd failed: %v", err)
	}

	// 3. Test Connection Error
	// Reset flags manually or use new args
	_ = metricsCmd.Flags().Set("api-url", "http://localhost:0")
	// Execute Run methods directly if possible or Execute?
	// Execute parses args. SetArgs sets what to parse.
	// If we use SetArgs, we must ensure strict flag parsing.

	// Better approach: Test RunE function directly?
	// But RunE signature (cmd, args) logic reads flags from cmd.
	// So setting flags is correct.

	if err := metricsCmd.RunE(metricsCmd, nil); err == nil {
		t.Error("Expected error for invalid URL")
	}

	// 4. Test Server Error (500)
	tsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tsErr.Close()

	_ = metricsCmd.Flags().Set("api-url", tsErr.URL)
	if err := metricsCmd.RunE(metricsCmd, nil); err == nil {
		t.Error("Expected error for 500")
	}
}

func TestKeysCommands(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "keys-test")
	defer os.RemoveAll(tmpDir)

	// 1. Gen Cluster
	// fluxrig keys gen-cluster -d tmpDir
	_ = keysGenClusterCmd.Flags().Set("dir", tmpDir)
	_ = keysGenClusterCmd.Flags().Set("name", "test-cluster.key")

	if err := keysGenClusterCmd.RunE(keysGenClusterCmd, nil); err != nil {
		t.Errorf("Gen cluster failed: %v", err)
	}

	keyPath := filepath.Join(tmpDir, "test-cluster.key")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("Private key not created")
	}
	if _, err := os.Stat(keyPath + ".pub"); os.IsNotExist(err) {
		t.Error("Public key not created")
	}

	// 2. Mock State Envelope for Inspect
	// Create a dummy identity signed by this key
	// We need pki package
	// We can cheat and just ensure inspect fails gracefully on bad file
	// Or create real one.
	// Since pki is internal, we can use it? pki is in pkg/pki.
	// We are in commands/commands_test (package commands).

	// Let's create a real envelope to test success path.
	pubC, privC, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: privC, Public: pubC}

	state := pki.RackState{
		MachineID:   101,
		Name:        "test-rack",
		Status:      "active",
		Secret:      "deadbeef",
		MixerPublic: pubC,
	}
	env, err := signer.Sign(&state)
	if err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(tmpDir, "state.flux")
	if err := env.Save(envPath); err != nil {
		t.Fatal(err)
	}

	// 3. Inspect
	// Inspect command takes path as arg[0].
	// Cobra sets args via SetArgs equivalent for RunE? No, RunE(cmd, args).
	if err := keysInspectCmd.RunE(keysInspectCmd, []string{envPath}); err != nil {
		t.Errorf("Inspect failed: %v", err)
	}
}

func TestAdminRacksCommands(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api/v1/racks" && r.Method == "GET":
			// List
			fmt.Fprintln(w, `[{"machine_id":1,"name":"rack-1","status":"active"}]`)
		case path == "/api/v1/racks/1/approve" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/1/suspend" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/1/activate" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/1" && r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// 1. List
	_ = racksListCmd.Flags().Set("api-url", ts.URL)
	if err := racksListCmd.RunE(racksListCmd, nil); err != nil {
		t.Errorf("List failed: %v", err)
	}

	// 2. Approve
	_ = racksApproveCmd.Flags().Set("api-url", ts.URL)
	_ = racksApproveCmd.Flags().Set("name", "rack-1")
	if err := racksApproveCmd.RunE(racksApproveCmd, []string{"1"}); err != nil {
		t.Errorf("Approve failed: %v", err)
	}

	// 3. Suspend
	_ = racksSuspendCmd.Flags().Set("api-url", ts.URL)
	if err := racksSuspendCmd.RunE(racksSuspendCmd, []string{"1"}); err != nil {
		t.Errorf("Suspend failed: %v", err)
	}

	// 4. Activate
	_ = racksActivateCmd.Flags().Set("api-url", ts.URL)
	if err := racksActivateCmd.RunE(racksActivateCmd, []string{"1"}); err != nil {
		t.Errorf("Activate failed: %v", err)
	}

	// 5. Remove
	_ = racksRemoveCmd.Flags().Set("api-url", ts.URL)
	if err := racksRemoveCmd.RunE(racksRemoveCmd, []string{"1"}); err != nil {
		t.Errorf("Remove failed: %v", err)
	}
}

func TestMaskSecret(t *testing.T) {
	// Test short secret
	short := maskSecret("abc")
	if short != "****" {
		t.Errorf("Expected ****, got %s", short)
	}

	// Test long secret
	long := maskSecret("1234567890abcdef")
	if long != "1234...cdef" {
		t.Errorf("Expected 1234...cdef, got %s", long)
	}
}
