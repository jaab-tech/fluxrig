// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	fmt.Printf("Status     : %s\n", state.Status)
	fmt.Printf("MixerID    : 0x%x\n", state.MixerID)
	fmt.Printf("MixerPub   : %s (Verified)\n", hex.EncodeToString(state.MixerPublic))
}
