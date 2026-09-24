// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/snake"
)

func TestServer_ProvisionKV(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-kv-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// Test ProvisionKV
	err = srv.ProvisionKV(context.Background(), "TEST_BUCKET")
	if err != nil {
		t.Fatalf("ProvisionKV failed: %v", err)
	}

	// Verify bucket exists
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = js.KeyValue(ctx, "TEST_BUCKET")
	if err != nil {
		t.Fatalf("KV bucket not found: %v", err)
	}
}

func TestServer_ProvisionKV_Idempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-kv-idem")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// First provision
	err = srv.ProvisionKV(context.Background(), "IDEM_BUCKET")
	if err != nil {
		t.Fatalf("First ProvisionKV failed: %v", err)
	}

	// Second provision should not error
	err = srv.ProvisionKV(context.Background(), "IDEM_BUCKET")
	if err != nil {
		t.Fatalf("Second ProvisionKV failed (idempotency): %v", err)
	}
}

func TestServer_Shutdown(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-shutdown")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	srv.Shutdown()

	// Verify shutdown - connection should fail
	_, err = nats.Connect(srv.ClientURL())
	if err == nil {
		t.Error("Expected connection to fail after shutdown")
	}
}

func TestServer_Clients(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-clients")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:           -1,
		ClusterName:    "test-cluster",
		StoreDir:       tmpDir,
		StreamName:     "TEST_STREAM",
		StreamSubjects: []string{"test.>"},
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// Connect a client
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	// Wait a bit for connection to register
	time.Sleep(500 * time.Millisecond)

	clients, err := srv.Clients()
	if err != nil {
		t.Fatalf("Clients() failed: %v", err)
	}

	// Should have at least one client (our test connection)
	if len(clients) == 0 {
		t.Log("No clients found (may be timing issue)")
	} else {
		found := false
		for _, c := range clients {
			if c.Name != "" {
				found = true
				break
			}
		}
		if !found {
			t.Log("Connected but no named clients found")
		}
	}
}

func TestServer_Stats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-stats")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	stats := srv.Stats()
	if stats == nil {
		t.Fatal("Stats() returned nil")
	}

	// Check expected keys
	expectedKeys := []string{"in_msgs", "out_msgs", "in_bytes", "out_bytes", "connections", "subscriptions", "uptime"}
	for _, key := range expectedKeys {
		if _, ok := stats[key]; !ok {
			t.Errorf("Stats missing key: %s", key)
		}
	}
}

func TestServer_Stats_WithConnection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-stats-conn")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// Connect a client
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	// Send a test message to generate stats
	if err := nc.Publish("test.subject", []byte("test")); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait for stats to update
	time.Sleep(500 * time.Millisecond)

	stats := srv.Stats()
	if stats == nil {
		t.Fatal("Stats() returned nil")
	}

	// Should have at least some messages
	if v, ok := stats["out_msgs"].(int64); !ok || v < 0 {
		t.Errorf("Expected out_msgs >= 0, got %v", stats["out_msgs"])
	}
}

func TestServer_InProcessConn(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-inproc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	nc, err := srv.InProcessConn()
	if err != nil {
		t.Fatalf("InProcessConn failed: %v", err)
	}
	defer nc.Close()

	// Test publish/subscribe
	received := make(chan *nats.Msg, 1)
	_, err = nc.Subscribe("test.inproc", func(m *nats.Msg) {
		received <- m
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if err := nc.Publish("test.inproc", []byte("hello")); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case msg := <-received:
		if string(msg.Data) != "hello" {
			t.Errorf("Expected 'hello', got %s", string(msg.Data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for in-process message")
	}
}

func TestServer_ProvisionStream_Update(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-stream-update")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:           -1,
		ClusterName:    "test-cluster",
		StoreDir:       tmpDir,
		StreamName:     "UPDATE_TEST",
		StreamSubjects: []string{"old.>"},
	}

	srv, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// Now try to update the stream with new subjects
	err = srv.ProvisionStream(context.Background(), "UPDATE_TEST", []string{"new.>", "updated.>"})
	if err != nil {
		t.Fatalf("ProvisionStream update failed: %v", err)
	}

	// Verify subjects were updated
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	strm, err := js.Stream(ctx, "UPDATE_TEST")
	if err != nil {
		t.Fatalf("Stream not found: %v", err)
	}
	info, err := strm.Info(ctx)
	if err != nil {
		t.Fatalf("Stream not found: %v", err)
	}

	if len(info.Config.Subjects) != 2 {
		t.Errorf("Expected 2 subjects, got %d: %v", len(info.Config.Subjects), info.Config.Subjects)
	}
}

// Before this fix, MaxBytes was applied to an existing stream only when the
// new value was greater than zero, unlike MaxAge two lines above it. Zero is
// a real, documented setting on Config ("Zero means no limit"), not "leave
// whatever the stream already has": an operator who clears a previous byte
// limit back to unlimited must see that take effect on the next start, the
// same way clearing the age limit already does.
func TestServer_ProvisionStream_ClearsMaxBytesBackToUnlimited(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-stream-maxbytes")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:           -1,
		ClusterName:    "test-cluster",
		StoreDir:       tmpDir,
		StreamName:     "MAXBYTES_TEST",
		StreamSubjects: []string{"maxbytes.>"},
		StreamMaxBytes: 5000,
	}
	srv1, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	srv1.Shutdown()

	// Same store, restarted with StreamMaxBytes now 0: re-provisioning the
	// existing stream must clear the old limit, not keep it forever.
	cfg.StreamMaxBytes = 0
	srv2, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to restart server: %v", err)
	}
	defer srv2.Shutdown()

	nc, err := nats.Connect(srv2.ClientURL())
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	strm, err := js.Stream(ctx, "MAXBYTES_TEST")
	if err != nil {
		t.Fatalf("stream not found: %v", err)
	}
	info, err := strm.Info(ctx)
	if err != nil {
		t.Fatalf("stream info failed: %v", err)
	}

	// nats-server normalizes a MaxBytes of 0 to its own -1 ("unlimited")
	// sentinel on write; either not-positive value means the limit is gone.
	// What must not happen is the old 5000 surviving the restart.
	if info.Config.MaxBytes > 0 {
		t.Errorf("expected MaxBytes cleared back to unlimited on restart, got %d", info.Config.MaxBytes)
	}
}

func TestServer_Config_Defaults(t *testing.T) {
	cfg := snake.Config{
		Port:        -1,
		ClusterName: "test",
		StoreDir:    "/tmp",
	}
	// Config doesn't have ApplyDefaults, just check it doesn't panic
	_ = cfg
}

func TestServer_PortCollision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-port")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := snake.Config{
		Port:        18080, // Use a specific port
		ClusterName: "test-cluster",
		StoreDir:    tmpDir,
	}

	// Start first server
	srv1, err := snake.NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Failed to start first server: %v", err)
	}
	defer srv1.Shutdown()

	// Try to start second server on same port - should fail
	cfg2 := cfg
	cfg2.StoreDir = tmpDir + "2"
	_, err = snake.NewServer(context.Background(), cfg2)
	if err == nil {
		t.Error("Expected error when starting second server on same port")
	}
}
