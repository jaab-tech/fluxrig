package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	// 1. Generate Keypair (Simulating Mixer)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	// 2. Create Verifier (Simulating Rack)
	v, err := NewVerifier(pub)
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}

	// 3. Sign Data
	data := []byte("configuration-blob")
	sig := ed25519.Sign(priv, data)

	// 4. Verify
	if !v.Verify(data, sig) {
		t.Error("Signature validation failed")
	}

	// 5. Tamper
	tampered := []byte("configuration-blob-modified")
	if v.Verify(tampered, sig) {
		t.Error("Validation passed on tampered data")
	}
}

func TestLoadVerifierFromFile(t *testing.T) {
	// 1. Generate Keypair
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// 2. Write to Temp File
	tmpfile, err := os.CreateTemp("", "cluster.pub")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(pub); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. Load
	v, err := LoadVerifierFromFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(v.clusterPubKey) != 32 {
		t.Error("Public key loaded incorrectly")
	}
}

func TestVerifier_Errors(t *testing.T) {
	// 1. NewVerifier Invalid Size
	if _, err := NewVerifier([]byte("short")); err == nil {
		t.Error("Expected error for short key, got nil")
	}

	// 2. LoadVerifierFromFile Invalid Size
	tmpfile, _ := os.CreateTemp("", "badkey.pub")
	defer os.Remove(tmpfile.Name())
	_, _ = tmpfile.Write([]byte("short"))
	_ = tmpfile.Close()

	if _, err := LoadVerifierFromFile(tmpfile.Name()); err == nil {
		t.Error("Expected error for short key file, got nil")
	}

	// 3. LoadVerifierFromFile Not Found
	if _, err := LoadVerifierFromFile("missing.pub"); err == nil {
		t.Error("Expected error for missing file, got nil")
	}

	// 4. Verify Invalid Sig Size
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := NewVerifier(pub)
	if v.Verify([]byte("data"), []byte("short-sig")) {
		t.Error("Verified passed with invalid signature size")
	}
}

func TestMaskPAN(t *testing.T) {
	// Valid PAN
	masked := MaskPAN("4111111111111111")
	if masked != "411111******1111" {
		t.Errorf("Expected 411111******1111, got %s", masked)
	}
	
	// Short PAN
	short := MaskPAN("12345")
	if short != "************" {
		t.Errorf("Expected ************, got %s", short)
	}
}

func TestMaskCVV(t *testing.T) {
	// Any CVV should be masked
	masked := MaskCVV("123")
	if masked != "***" {
		t.Errorf("Expected ***, got %s", masked)
	}
}
