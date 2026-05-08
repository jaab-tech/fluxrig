// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func TestTelemetryCommands(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1/telemetry/logs" {
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

	_ = logsCmd.Flags().Set("api-url", ts.URL)
	_ = logsCmd.Flags().Set("limit", "1")
	_ = logsCmd.Flags().Set("min-level", "INFO")

	if err := logsCmd.RunE(logsCmd, nil); err != nil {
		t.Errorf("logsCmd failed: %v", err)
	}

	_ = metricsCmd.Flags().Set("api-url", ts.URL)
	_ = metricsCmd.Flags().Set("limit", "1")
	_ = metricsCmd.Flags().Set("name", "cpu")

	if err := metricsCmd.RunE(metricsCmd, nil); err != nil {
		t.Errorf("metricsCmd failed: %v", err)
	}
}

func TestKeysCommands(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "keys-test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	_ = keysGenClusterCmd.Flags().Set("dir", tmpDir)
	_ = keysGenClusterCmd.Flags().Set("name", "test-cluster.key")

	if err := keysGenClusterCmd.RunE(keysGenClusterCmd, nil); err != nil {
		t.Errorf("Gen cluster failed: %v", err)
	}

	keyPath := filepath.Join(tmpDir, "test-cluster.key")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("Private key not created")
	}

	pubC, privC, _ := ed25519.GenerateKey(rand.Reader)
	signer := &pki.ClusterKey{Private: privC, Public: pubC}

	machineID := uuid.New()
	state := pki.RackState{
		MachineID:   machineID,
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
	_ = env.Save(envPath)

	if err := keysInspectCmd.RunE(keysInspectCmd, []string{envPath}); err != nil {
		t.Errorf("Inspect failed: %v", err)
	}
}

func TestAdminRacksCommands(t *testing.T) {
	machineID := uuid.New()
	idStr := machineID.String()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api/v1/racks" && r.Method == "GET":
			_, _ = fmt.Fprintf(w, `[{"machine_id":"%s","name":"rack-1","status":"active"}]`, idStr)
		case path == "/api/v1/racks/"+idStr+"/approve" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/"+idStr+"/suspend" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/"+idStr+"/activate" && r.Method == "POST":
			w.WriteHeader(http.StatusOK)
		case path == "/api/v1/racks/"+idStr && r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	_ = racksListCmd.Flags().Set("api-url", ts.URL)
	if err := racksListCmd.RunE(racksListCmd, nil); err != nil {
		t.Errorf("List failed: %v", err)
	}

	_ = racksApproveCmd.Flags().Set("api-url", ts.URL)
	_ = racksApproveCmd.Flags().Set("name", "rack-1")
	if err := racksApproveCmd.RunE(racksApproveCmd, []string{idStr}); err != nil {
		t.Errorf("Approve failed: %v", err)
	}

	_ = racksSuspendCmd.Flags().Set("api-url", ts.URL)
	if err := racksSuspendCmd.RunE(racksSuspendCmd, []string{idStr}); err != nil {
		t.Errorf("Suspend failed: %v", err)
	}

	_ = racksActivateCmd.Flags().Set("api-url", ts.URL)
	if err := racksActivateCmd.RunE(racksActivateCmd, []string{idStr}); err != nil {
		t.Errorf("Activate failed: %v", err)
	}

	_ = racksRemoveCmd.Flags().Set("api-url", ts.URL)
	if err := racksRemoveCmd.RunE(racksRemoveCmd, []string{idStr}); err != nil {
		t.Errorf("Remove failed: %v", err)
	}
}

func TestMaskSecret(t *testing.T) {
	short := maskSecret("abc")
	if short != "****" {
		t.Errorf("Expected ****, got %s", short)
	}

	long := maskSecret("1234567890abcdef")
	if long != "1234...cdef" {
		t.Errorf("Expected 1234...cdef, got %s", long)
	}
}
