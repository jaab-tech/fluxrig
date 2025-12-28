package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	_ "github.com/marcboeker/go-duckdb"
	"github.com/nats-io/nats.go"
)

// TestDualHead verifies the Mixer starts, Embeds NATS, and Initializes DuckDB.
func TestDualHead(t *testing.T) {
	// 1. Setup Paths
	wd, _ := os.Getwd()
	root := filepath.Dir(filepath.Dir(wd)) // ../.. from test/e2e
	binMixer := filepath.Join(root, "bin", "fluxrig-mixer")
	configPath := filepath.Join(root, "test", "fluxrig_mixer.toml")

	// Ensure cleanup of previous data
	os.RemoveAll(filepath.Join(root, "data", "fluxrig_test.duckdb"))
	os.RemoveAll(filepath.Join(root, "data", "js")) // JetStream data
	if err := os.MkdirAll(filepath.Join(root, "data"), 0755); err != nil {
		t.Fatalf("Failed to create data dir: %v", err)
	}

	// Generate Cluster Key using the library directly (avoids binary dependency)
	keyPath := filepath.Join(root, "data", "test_cluster.key")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate key: %v", err)
		}
		ck := &pki.ClusterKey{Private: priv, Public: pub}
		if err := ck.Save(keyPath); err != nil {
			t.Fatalf("Failed to save cluster key: %v", err)
		}
	}

	// 2. Start Mixer
	// Skip if binary not built (happens after make clean)
	if _, err := os.Stat(binMixer); os.IsNotExist(err) {
		t.Skip("Skipping E2E: binaries not built (run 'make build' first)")
	}

	cmd := exec.Command(binMixer, "-c", configPath)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start mixer: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// 3. Wait for Readiness
	t.Log("Waiting for Mixer to start...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ready := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for Mixer")
		default:
			// check NATS port connectivity logic or API
			resp, err := http.Get("http://localhost:8091")
			// We expect 404 perhaps, but if connection refused, it's not ready.
			if err == nil {
				resp.Body.Close()
				ready = true
			}
		}
		if ready {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !ready {
		t.Fatal("Mixer failed to become ready")
	}

	// 4. Verify Embedded NATS (Snake)
	t.Log("Verifying Embedded NATS...")
	nc, err := nats.Connect("nats://localhost:4223")
	if err != nil {
		t.Fatalf("Failed to connect to Embedded NATS: %v", err)
	}
	defer nc.Close()
	t.Log("Connected to Snake Protocol!")

	// 5. Verify DuckDB Persistence
	t.Log("Verifying DuckDB Schema...")
	dbPath := filepath.Join(root, "data", "fluxrig_test.duckdb")

	// DuckDB allows strictly one writer. If Mixer is running, we cannot open in R/W mode.
	// We verify file existence first.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("DuckDB file not created at %s", dbPath)
	}

	// To verify content while locked:
	// Option A: Use Read-Only mode (requires DuckDB version support for concurrent read).
	// Option B: Kill Mixer, then check DB.

	t.Log("Killing Mixer to inspect DB...")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	time.Sleep(1 * time.Second) // Release lock

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DuckDB: %v", err)
	}
	defer db.Close()

	// Check table existence
	var count int
	row := db.QueryRow("SELECT count(*) FROM racks")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("Failed to query racks table (Schema Init failed?): %v", err)
	}
	t.Logf("Racks table exists (Row count: %d)", count)
}
