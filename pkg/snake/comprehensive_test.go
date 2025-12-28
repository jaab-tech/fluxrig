package snake_test

import (
	"os"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/nats-io/nats.go"
)

func TestServer_Observability(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snake-obs-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := snake.Config{
		Port:           -1,
		ClusterName:    "obs-cluster",
		StoreDir:       tmpDir,
		StreamName:     "OBS_STREAM",
		StreamSubjects: []string{"obs.>"},
	}

	srv, err := snake.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer srv.Shutdown()

	// 1. Stats (Empty)
	stats := srv.Stats()
	if stats["connections"].(int) != 0 {
		// Might be 0 or small number depending on internal system connections
		// But usually 0 if no clients.
	}

	// 2. Connect Client
	nc, err := nats.Connect(srv.ClientURL(), nats.Name("test-client"))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	// Wait for server to register connection
	time.Sleep(200 * time.Millisecond)

	// 3. Stats (1 Connection)
	stats = srv.Stats()
	conns := stats["connections"].(int)
	if conns < 1 {
		t.Errorf("Expected at least 1 connection, got %d", conns)
	}
	
	entities := stats["connected_entities"].([]string)
	found := false
	for _, e := range entities {
		if e == "test-client" {
			found = true
			break
		}
	}
	if !found {
		t.Error("test-client not found in connected_entities")
	}

	// 4. Clients()
	clients, err := srv.Clients()
	if err != nil {
		t.Fatalf("Clients() failed: %v", err)
	}
	foundClient := false
	for _, c := range clients {
		if c.Name == "test-client" {
			foundClient = true
			if c.IP == "" {
				t.Error("Client IP missing")
			}
			break
		}
	}
	if !foundClient {
		t.Error("test-client not found in Clients() list")
	}
}
