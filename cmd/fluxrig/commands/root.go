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
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/spf13/cobra"
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
