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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	scenarioFile string
	dryRun       bool
)

var scenarioCmd = &cobra.Command{
	Use:   "scenario",
	Short: "Manage system scenarios (GitOps)",
}

var scenarioImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import and validate a scenario file",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 1. Read File
		content, err := os.ReadFile(filepath.Clean(scenarioFile))
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// 2. Send to Mixer
		// Assume Mixer URL is configured or default localhost:9000
		// In Phase 3, we might hardcode or read from ~/.fluxrig/config
		mixerURL := "http://localhost:9000/api/v1/scenario/import"
		if dryRun {
			mixerURL += "?dry_run=true"
		}

		resp, err := http.Post(mixerURL, "application/yaml", bytes.NewReader(content))
		if err != nil {
			return fmt.Errorf("failed to contact mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("import failed (%d): %s", resp.StatusCode, string(body))
		}

		fmt.Printf("Success: %s\n", string(body))
		return nil
	},
}

func init() {
	scenarioImportCmd.Flags().StringVarP(&scenarioFile, "file", "f", "", "Scenario YAML file")
	scenarioImportCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Validate without applying")
	_ = scenarioImportCmd.MarkFlagRequired("file")

	scenarioCmd.AddCommand(scenarioImportCmd)
	rootCmd.AddCommand(scenarioCmd) // Assuming rootCmd is exported or we register in root.go
}
