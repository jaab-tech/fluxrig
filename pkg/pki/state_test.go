package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClusterKey_Lifecycle(t *testing.T) {
	// 1. Generate Key
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	ck := &ClusterKey{
		Private: priv,
		Public:  pub,
	}

	// 2. Save
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "cluster.key")

	if err := ck.Save(keyPath); err != nil {
		t.Fatalf("Failed to save key: %v", err)
	}

	// 3. Load
	loadedCk, err := LoadClusterKey(keyPath)
	if err != nil {
		t.Fatalf("Failed to load key: %v", err)
	}

	// 4. Verify
	if !reflect.DeepEqual(ck.Private, loadedCk.Private) {
		t.Error("Private key mismatch")
	}
	if !reflect.DeepEqual(ck.Public, loadedCk.Public) {
		t.Error("Public key mismatch")
	}
}

func TestLoadClusterKey_Variations(t *testing.T) {
	tmpDir := t.TempDir()

	// Case A: Hex Encoded
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	hexPath := filepath.Join(tmpDir, "hex.key")
	hexData := hex.EncodeToString(priv)
	if err := os.WriteFile(hexPath, []byte(hexData), 0600); err != nil {
		t.Fatal(err)
	}

	ck, err := LoadClusterKey(hexPath)
	if err != nil {
		t.Fatalf("Failed to load hex key: %v", err)
	}
	if !reflect.DeepEqual(ck.Private, priv) {
		t.Error("Hex private key mismatch")
	}
	if !reflect.DeepEqual(ck.Public, pub) {
		t.Error("Hex public key mismatch")
	}

	// Case B: Seed (32 bytes)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	seedPriv := ed25519.NewKeyFromSeed(seed)
	seedPath := filepath.Join(tmpDir, "seed.key")
	if err := os.WriteFile(seedPath, seed, 0600); err != nil {
		t.Fatal(err)
	}

	ckSeed, err := LoadClusterKey(seedPath)
	if err != nil {
		t.Fatalf("Failed to load seed key: %v", err)
	}
	if !reflect.DeepEqual(ckSeed.Private, seedPriv) {
		t.Error("Seed private key mismatch")
	}
}

func TestPassport_Flow(t *testing.T) {
	// Setup Authority
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	authority := &ClusterKey{Private: priv, Public: pub}

	// Create State
	originalState := &RackState{
		ClusterID:     "cluster-alpha",
		MachineID:     42,
		Name:          "rack-42",
		Status:        "active",
		Secret:        "super-secret",
		ClusterPublic: pub,
	}

	// 1. Sign
	env, err := authority.Sign(originalState)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if len(env.Signature) == 0 {
		t.Error("Signature empty")
	}
	if len(env.Payload) == 0 {
		t.Error("Payload empty")
	}

	// 2. Verify Success
	recoveredState, err := env.Verify()
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !reflect.DeepEqual(originalState, recoveredState) {
		t.Errorf("State mismatch. Want %+v, got %+v", originalState, recoveredState)
	}

	// 3. Verify Tampering (Payload)
	// Flip a bit in the payload
	env.Payload[0] ^= 0xFF
	if _, err := env.Verify(); err == nil {
		t.Error("Verify should fail on tampered payload")
	}
	env.Payload[0] ^= 0xFF // Restore

	// 4. Verify Tampering (Signature)
	env.Signature[0] ^= 0xFF
	if _, err := env.Verify(); err == nil {
		t.Error("Verify should fail on tampered signature")
	}
}

func TestStateEnvelope_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "state.flux")

	env := &StateEnvelope{
		Payload:   []byte("test-payload"),
		Signature: []byte("test-sig"),
	}

	// Save
	if err := env.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load
	loadedEnv, err := LoadStateEnvelope(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !reflect.DeepEqual(env, loadedEnv) {
		t.Error("Envelope persistence mismatch")
	}
}

func TestLoadClusterKey_Errors(t *testing.T) {
	// Missing file
	if _, err := LoadClusterKey("non-existent"); err == nil {
		t.Error("Expected error for missing file")
	}

	// Invalid content length
	tmp := filepath.Join(os.TempDir(), "bad.key")
	if err := os.WriteFile(tmp, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)

	if _, err := LoadClusterKey(tmp); err == nil {
		t.Error("Expected error for invalid key length")
	}
}
