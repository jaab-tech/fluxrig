package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/pki"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: inspect_state <path_to_state.flux>")
		os.Exit(1)
	}

	path := os.Args[1]
	env, err := pki.LoadStateEnvelope(path)
	if err != nil {
		fmt.Printf("Error: failed to load envelope: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Envelope Loaded\n")
	fmt.Printf("Payload Size  : %d bytes\n", len(env.Payload))
	fmt.Printf("Signature Size: %d bytes\n", len(env.Signature))
	fmt.Printf("Signature Hex : %s\n", hex.EncodeToString(env.Signature))

	state, err := env.Verify()
	if err != nil {
		fmt.Printf("❌ Verification Failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Verification Passed! (Self-Signed Check)\n")
	fmt.Printf("--- Decoded Passport Content ---\n")
	fmt.Printf("MachineID  : %d\n", state.MachineID)
	fmt.Printf("Name       : %s\n", state.Name)
	fmt.Printf("ClusterID  : %s\n", state.ClusterID)
	fmt.Printf("ClusterPub : %s (Verified)\n", hex.EncodeToString(state.ClusterPublic))
}
