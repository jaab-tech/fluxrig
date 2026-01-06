package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"

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
		content, err := os.ReadFile(scenarioFile)
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
		defer resp.Body.Close()

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
