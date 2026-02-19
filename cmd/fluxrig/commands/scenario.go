// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
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

// scenarioImportCmd supports two modes:
//   - CAS import (default): stores the YAML directly into the local CAS store.
//   - API import (--api): sends the YAML to the running Mixer for hot-reload.
var scenarioImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import and validate a scenario file",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiMode, _ := cmd.Flags().GetBool("api")

		// 1. Read File
		content, err := os.ReadFile(filepath.Clean(scenarioFile))
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		if apiMode {
			// --- API mode: POST to running Mixer ---
			mixerURL := "http://localhost:9000/api/v1/scenario/import"
			if dryRun {
				mixerURL += "?dry_run=true"
			}

			req, errReq := http.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				mixerURL,
				bytes.NewReader(content),
			)
			if errReq != nil {
				return fmt.Errorf("failed to create request: %w", errReq)
			}
			req.Header.Set("Content-Type", "application/yaml")

			client := &http.Client{Timeout: 30 * time.Second}
			resp, errPost := client.Do(req)
			if errPost != nil {
				return fmt.Errorf("failed to contact mixer: %w", errPost)
			}
			defer func() { _ = resp.Body.Close() }()

			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("import failed (%d): %s", resp.StatusCode, string(body))
			}

			fmt.Printf("API import success: %s\n", string(body))
			return nil
		}

		// --- CAS mode: store locally ---
		name, _ := cmd.Flags().GetString("name")
		tag, _ := cmd.Flags().GetString("tag")
		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		hash, finalName, finalTag, err := mgr.ImportScenario(context.Background(), scenarioFile, name, tag)
		if err != nil {
			return err
		}

		fmt.Printf("Imported %s -> %s\n", cas.Ref(finalName, finalTag, hash), hash)
		return nil
	},
}

var scenarioListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stored scenarios",
	RunE: func(cmd *cobra.Command, args []string) error {
		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		list, err := mgr.List(context.Background())
		if err != nil {
			return err
		}

		fmt.Println("NAME\tTAG\tHASH")
		for _, a := range list {
			fmt.Printf("%s\t%s\t%s\n", a.Name, a.Tag, a.Hash)
		}
		return nil
	},
}

var scenarioExportCmd = &cobra.Command{
	Use:   "export <name:tag> <output-file>",
	Short: "Export a CAS scenario to a local file",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		urn := args[0]
		outputPath := args[1]

		storePath, _ := cmd.Flags().GetString("store-dir")
		if storePath == "" {
			home, _ := os.UserHomeDir()
			storePath = filepath.Join(home, ".fluxrig", "store")
		}

		mgr, err := manager.NewManager(storePath)
		if err != nil {
			return err
		}

		if err := mgr.Export(context.Background(), urn, outputPath); err != nil {
			return err
		}

		fmt.Printf("Exported %s -> %s\n", urn, outputPath)
		return nil
	},
}

func init() {
	// Persistent flag inherited by all subcommands
	scenarioCmd.PersistentFlags().String("store-dir", "", "CAS store directory (default: ~/.fluxrig/store)")

	scenarioImportCmd.Flags().StringVarP(&scenarioFile, "file", "f", "", "Scenario YAML file")
	scenarioImportCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Validate without applying")
	scenarioImportCmd.Flags().Bool("api", false, "Send to running Mixer API instead of CAS store")
	scenarioImportCmd.Flags().String("name", "", "Logical name for the scenario")
	scenarioImportCmd.Flags().String("tag", "", "Version tag (e.g. v1.0.0)")
	_ = scenarioImportCmd.MarkFlagRequired("file")

	scenarioCmd.AddCommand(scenarioImportCmd)
	scenarioCmd.AddCommand(scenarioListCmd)
	scenarioCmd.AddCommand(scenarioExportCmd)
	rootCmd.AddCommand(scenarioCmd) // Assuming rootCmd is exported or we register in root.go
}
