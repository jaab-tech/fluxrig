// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
)

// keysCmd represents the keys command
var keysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage cryptographic keys",
	Long:  `Generate and manage Ed25519 keys for the Offline Trust root.`,
}

// keysGenClusterCmd generates a new Cluster Keypair
var keysGenClusterCmd = &cobra.Command{
	Use:   "gen-cluster",
	Short: "Generate a new Cluster Authority Keypair",
	RunE: func(cmd *cobra.Command, args []string) error {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("out")
		dir, _ := cmd.Flags().GetString("dir")
		name, _ := cmd.Flags().GetString("name")

		// Determine Output Path
		var finalPath string
		if out != "" {
			finalPath = out
		} else {
			if dir == "" {
				dir = "." // Default to current directory
			}
			if name == "" {
				name = "cluster.key" // Default name
			}
			finalPath = fmt.Sprintf("%s/%s", dir, name)
		}

		// Ensure full path resolution for user clarity
		absPath, err := filepath.Abs(finalPath)
		if err != nil {
			absPath = finalPath // Fallback
		}

		safePath, errSan := path.Sanitize(finalPath)
		if errSan != nil {
			return fmt.Errorf("invalid path: %w", errSan)
		}
		finalPath = safePath

		// If dir is specified, ensure it exists
		if dir != "" {
			if err := os.MkdirAll(dir, 0750); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}

		// Write to File (Private Key)
		// We use the PKI helper to save just the private key (standard format)
		ck := &pki.ClusterKey{Private: priv, Public: pub}
		if err := ck.Save(finalPath); err != nil {
			return fmt.Errorf("failed to save private key: %w", err)
		}
		fmt.Printf("✅ Cluster Private Key saved to: %s\n", absPath)

		// Write Public Key (Optional convenience)
		pubPath := finalPath + ".pub"
		pubAbsPath, _ := filepath.Abs(pubPath)

		if err := os.WriteFile(pubPath, pub, 0600); err != nil {
			fmt.Printf("⚠️ Failed to same public key: %v\n", err)
		} else {
			fmt.Printf("✅ Cluster Public Key saved to:  %s\n", pubAbsPath)
		}

		return nil
	},
}

func init() {
	keysGenClusterCmd.Flags().StringP("out", "o", "", "Full output path (overrides dir/name)")
	keysGenClusterCmd.Flags().StringP("dir", "d", ".", "Output directory")
	keysGenClusterCmd.Flags().StringP("name", "n", "cluster.key", "Filename for the key")

	rootCmd.AddCommand(keysCmd)
	keysCmd.AddCommand(keysGenClusterCmd)
	keysCmd.AddCommand(keysInspectCmd)
}

// keysInspectCmd inspects a state.flux file
var keysInspectCmd = &cobra.Command{
	Use:   "inspect [path]",
	Short: "Inspect a State Envelope (state.flux)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p := args[0]
		safePath, errSan := path.Sanitize(p)
		if errSan != nil {
			return fmt.Errorf("invalid path: %w", errSan)
		}

		env, err := pki.LoadStateEnvelope(safePath)
		if err != nil {
			return fmt.Errorf("failed to load state envelope: %w", err)
		}

		fmt.Printf("Envelope Loaded: %s\n", safePath)

		// 1. Try Rack Verification (Embedded Key)
		state, err := env.Verify()
		if err == nil {
			fmt.Printf("✅ Signature Verification Passed (Rack Passport)\n")
			displayRackIdentity(state)
			return nil
		}

		// 2. Try Mixer Verification (Requires Authority Key)
		// Try to find cluster.key in the same dir as the passport
		dir := filepath.Dir(safePath)
		clusterKeyPath := filepath.Join(dir, "cluster.key")
		if _, errStat := os.Stat(clusterKeyPath); errStat != nil {
			// Try current dir
			clusterKeyPath = "cluster.key"
		}

		ck, errLoad := pki.LoadClusterKey(clusterKeyPath)
		if errLoad == nil {
			mState, errVer := env.VerifyMixer(ck.Public)
			if errVer == nil {
				fmt.Printf("✅ Signature Verification Passed (Mixer Authority)\n")
				displayMixerIdentity(mState)
				return nil
			}
		}

		// 3. If everything failed, try a blind decode (No Verification)
		fmt.Printf("❌ Signature Verification Failed\n")
		fmt.Printf("Attempting unverified decode...\n")

		// We use a local RackState struct to avoid verify logic
		var rState pki.RackState
		if errR := cbor.Unmarshal(env.Payload, &rState); errR == nil && rState.Name != "" {
			displayRackIdentity(&rState)
			return nil
		}

		var mState pki.MixerState
		if errM := cbor.Unmarshal(env.Payload, &mState); errM == nil && mState.Name != "" {
			displayMixerIdentity(&mState)
			return nil
		}

		return fmt.Errorf("failed to decode identity: %v", err)
	},
}

func displayRackIdentity(state *pki.RackState) {
	fmt.Printf("--------------------------------------------------\n")
	fmt.Printf("IDENTITY (RACK):\n")
	fmt.Printf("  MachineID: %s\n", state.MachineID)
	fmt.Printf("  Name:      %s\n", state.Name)
	fmt.Printf("  Status:    %s\n", state.Status)
	fmt.Printf("  Version:   %s\n", state.Version)
	fmt.Printf("  Secret:    %s\n", maskSecret(state.Secret))
	if len(state.MixerPublic) > 0 {
		fmt.Printf("  MixerKey:  %x\n", state.MixerPublic[:8])
	}
	if state.CreatedAt > 0 {
		fmt.Printf("  Created:   %s\n", time.Unix(state.CreatedAt, 0).Format(time.RFC3339))
	}
	if state.UpdatedAt > 0 {
		fmt.Printf("  Updated:   %s\n", time.Unix(state.UpdatedAt, 0).Format(time.RFC3339))
	}
	fmt.Printf("  Revision:  %d\n", state.UpdateCount)
	if state.ScenarioVer != "" {
		fmt.Printf("  Scenario:  %s\n", state.ScenarioVer)
	}
	fmt.Printf("--------------------------------------------------\n")
}

func displayMixerIdentity(state *pki.MixerState) {
	fmt.Printf("--------------------------------------------------\n")
	fmt.Printf("IDENTITY (MIXER):\n")
	fmt.Printf("  MachineID: %s\n", state.MachineID)
	fmt.Printf("  Name:      %s\n", state.Name)
	fmt.Printf("  Version:   %s\n", state.Version)
	fmt.Printf("  Created:   %s\n", time.Unix(state.CreatedAt, 0).Format(time.RFC3339))
	fmt.Printf("  Updated:   %s\n", time.Unix(state.UpdatedAt, 0).Format(time.RFC3339))
	fmt.Printf("  Revision:  %d\n", state.UpdateCount)
	fmt.Printf("--------------------------------------------------\n")
}

func maskSecret(s string) string {
	if len(s) < 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
