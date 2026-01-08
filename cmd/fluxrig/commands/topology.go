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
			baseURL = "http://localhost:9000"
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
			baseURL = "http://localhost:9000"
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
