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
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/manager/cas"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
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
	Use:   "import <file>",
	Short: "Import and validate a scenario file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiMode, _ := cmd.Flags().GetBool("api")
		scenarioFile = args[0]

		safePath, errSan := path.Sanitize(scenarioFile)
		if errSan != nil {
			return fmt.Errorf("invalid file path: %w", errSan)
		}

		// 1. Read File
		content, err := os.ReadFile(safePath)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		if apiMode {
			// --- API mode: POST to running Mixer ---
			baseURL := os.Getenv("FLUXRIG_API_URL")
			if baseURL == "" {
				baseURL = "http://localhost:8090"
			}
			mixerURL := fmt.Sprintf("%s/api/v1/scenario/import", baseURL)
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

var scenarioDiffCmd = &cobra.Command{
	Use:   "diff <scenario-file>",
	Short: "Show differences between a local file and the active scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scenarioFile = args[0]
		safePath, errSan := path.Sanitize(scenarioFile)
		if errSan != nil {
			return fmt.Errorf("invalid file path: %w", errSan)
		}

		// 1. Read Local File
		localContent, err := os.ReadFile(safePath)
		if err != nil {
			return fmt.Errorf("failed to read local file: %w", err)
		}

		// 2. Fetch Remote (Active) Scenario
		baseURL := os.Getenv("FLUXRIG_API_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8090"
		}
		activeURL := fmt.Sprintf("%s/api/v1/scenario/active", baseURL)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(activeURL)
		if err != nil {
			return fmt.Errorf("failed to fetch active scenario from mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("mixer returned error (%d): ensure a scenario is active", resp.StatusCode)
		}

		remoteContent, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read mixer response: %w", err)
		}

		// 3. Generate Diff
		diff := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(remoteContent)),
			B:        difflib.SplitLines(string(localContent)),
			FromFile: "Mixer (Active)",
			ToFile:   filepath.Base(scenarioFile),
			Context:  3,
		}

		text, _ := difflib.GetUnifiedDiffString(diff)
		if text == "" {
			fmt.Println(color.GreenString("✓ Local file matches active scenario."))
			return nil
		}

		// 4. Colorize and Print
		fmt.Println(color.CyanString("Scenario Diff:"))
		lines := strings.Split(text, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				fmt.Println(color.GreenString(line))
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				fmt.Println(color.RedString(line))
			} else if strings.HasPrefix(line, "@@") {
				fmt.Println(color.CyanString(line))
			} else {
				fmt.Println(line)
			}
		}

		return nil
	},
}

func init() {
	// Persistent flag inherited by all subcommands
	scenarioCmd.PersistentFlags().String("store-dir", "", "CAS store directory (default: ~/.fluxrig/store)")

	scenarioImportCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Validate without applying")
	scenarioImportCmd.Flags().Bool("api", false, "Send to running Mixer API instead of CAS store")
	scenarioImportCmd.Flags().String("name", "", "Logical name for the scenario")
	scenarioImportCmd.Flags().String("tag", "", "Version tag (e.g. v1.0.0)")

	scenarioCmd.AddCommand(scenarioImportCmd)

	scenarioCmd.AddCommand(scenarioListCmd)
	scenarioCmd.AddCommand(scenarioExportCmd)
	scenarioCmd.AddCommand(scenarioDiffCmd)
	rootCmd.AddCommand(scenarioCmd)
}
