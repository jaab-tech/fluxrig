// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var topologyCmd = &cobra.Command{
	Use:   "topology",
	Short: "Inspect the runtime topology",
}

var topologyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show global synchronization status",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseURL := os.Getenv("FLUXRIG_API_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8090"
		}
		mixerURL := fmt.Sprintf("%s/api/v1/topology/status", baseURL)

		resp, err := http.Get(mixerURL) // #nosec G107 -- URL is from trusted env var
		if err != nil {
			return fmt.Errorf("failed to contact mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status query failed (%d): %s", resp.StatusCode, string(body))
		}

		fmt.Println(string(body))
		return nil
	},
}

var topologyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active deployments",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseURL := os.Getenv("FLUXRIG_API_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8090"
		}
		mixerURL := fmt.Sprintf("%s/api/v1/topology/list", baseURL)
		resp, err := http.Get(mixerURL) // #nosec G107 -- URL is from trusted env var
		if err != nil {
			return fmt.Errorf("failed to contact mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		fmt.Println(string(body))
		return nil
	},
}

func init() {
	topologyCmd.AddCommand(topologyStatusCmd)
	topologyCmd.AddCommand(topologyListCmd)
	rootCmd.AddCommand(topologyCmd)
}
