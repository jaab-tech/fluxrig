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

package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/spf13/cobra"
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
		path := args[0]
		env, err := pki.LoadStateEnvelope(path)
		if err != nil {
			return fmt.Errorf("failed to load state envelope: %w", err)
		}

		fmt.Printf("Envelope Loaded: %s\n", path)

		// Verify the Passport Signature
		state, err := env.Verify()
		if err != nil {
			fmt.Printf("❌ Signature Verification Failed: %v\n", err)
		} else {
			fmt.Printf("✅ Signature Verification Passed\n")
		}

		// If verification passed, display the Identity contents

		if state != nil {
			fmt.Printf("--------------------------------------------------\n")
			fmt.Printf("IDENTITY:\n")
			fmt.Printf("  MachineID: %d\n", state.MachineID)
			fmt.Printf("  Name:      %s\n", state.Name)
			fmt.Printf("  Status:    %s\n", state.Status)
			fmt.Printf("  Secret:    %s\n", maskSecret(state.Secret))
			fmt.Printf("  MixerKey:%x\n", state.MixerPublic[:8]) // Show prefix
			fmt.Printf("--------------------------------------------------\n")
		}

		return nil
	},
}

func maskSecret(s string) string {
	if len(s) < 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
