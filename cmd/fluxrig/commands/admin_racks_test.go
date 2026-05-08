// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

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

	out, err := executeCommand(rootCmd, "admin", "racks", "suspend", id, "--api-url", mockServer.URL)
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

	out, err := executeCommand(rootCmd, "admin", "racks", "activate", id, "--api-url", mockServer.URL)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	if !strings.Contains(out, "activated") {
		t.Errorf("Expected 'activated' in output, got: %s", out)
	}
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

	out, err := executeCommand(rootCmd, "admin", "racks", "remove", id, "--api-url", mockServer.URL)
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if !strings.Contains(out, "removed") {
		t.Errorf("Expected 'removed' in output, got: %s", out)
	}
}
