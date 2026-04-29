// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/version"
)

var rootCmd = &cobra.Command{
	Use:   "fluxrig",
	Short: "fluxrig is the Unified Edge Node for the JAAB Tech platform",
	Long: `fluxrig acts as a deterministic, secure, and isolated execution unit 
for processing critical data at the edge. 

It connects to the centralized Mixer for configuration and oversight 
but executes independently based on signed state.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of fluxrig",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.FullInfo())
	},
}

// Execute is the entry point for the CLI.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().String("store-dir", "", "Path to spec store directory")
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.AddCommand(versionCmd)
}
