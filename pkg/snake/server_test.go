// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake_test

import (
	"os"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/nats-io/nats.go"
)

func TestNewServer_Provisioning(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "snake-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:           -1, // Random port
		ClusterName:    "test-cluster",
		StoreDir:       tmpDir,
		StreamName:     "TEST_STREAM",
		StreamSubjects: []string{"test.>"},
	}

	// 1. Start Server (Should provision stream)
	srv, err := snake.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// 2. Verify Stream Exists
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	// Use JetStream management to check
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}

	info, err := js.StreamInfo("TEST_STREAM")
	if err != nil {
		t.Fatalf("Stream not found: %v", err)
	}

	if info.Config.Name != "TEST_STREAM" {
		t.Errorf("Wrong stream name: %s", info.Config.Name)
	}
}

func TestNewServer_Idempotency(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "snake-test-idem")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:           -1,
		ClusterName:    "test-cluster",
		StoreDir:       tmpDir,
		StreamName:     "TEST_STREAM_2",
		StreamSubjects: []string{"test2.>"},
	}

	// 1. Start Server First Time
	srv1, err := snake.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv1.Shutdown()

	// 2. Restart Server (Should not fail on existing stream)
	srv2, err := snake.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to restart server (idempotency check): %v", err)
	}
	defer srv2.Shutdown()
}
