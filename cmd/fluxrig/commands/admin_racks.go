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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// adminRacksCmd represents the racks command group
var adminRacksCmd = &cobra.Command{
	Use:   "racks",
	Short: "Manage racks",
}

// racksListCmd represents the list command
var racksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered racks",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL, _ := cmd.Flags().GetString("api-url")
		resp, err := http.Get(apiURL + "/api/v1/racks")
		if err != nil {
			return fmt.Errorf("failed to connect to mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned error: %s", resp.Status)
		}

		var racks []struct {
			MachineID uint16 `json:"machine_id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			IP        string `json:"ip"`
			Port      int    `json:"port"`
			LastSeen  string `json:"last_seen"`
			FirstSeen string `json:"first_seen"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&racks); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATUS\tIP\tPORT\tLAST SEEN\tFIRST SEEN")
		for _, r := range racks {
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\t%s\n", r.MachineID, r.Name, r.Status, r.IP, r.Port, r.LastSeen, r.FirstSeen)
		}
		_ = w.Flush()
		return nil
	},
}

// racksApproveCmd represents the approve command
var racksApproveCmd = &cobra.Command{
	Use:   "approve [id]",
	Short: "Approve a pending rack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("name is required")
		}

		apiURL, _ := cmd.Flags().GetString("api-url")

		body := map[string]string{"name": name}
		jsonBody, _ := json.Marshal(body)

		resp, err := http.Post(
			fmt.Sprintf("%s/api/v1/racks/%s/approve", apiURL, id),
			"application/json",
			bytes.NewBuffer(jsonBody),
		)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("approve failed: %s", resp.Status)
		}

		fmt.Printf("✅ Rack %s approved as '%s'\n", id, name)
		return nil
	},
}

var racksSuspendCmd = &cobra.Command{
	Use:   "suspend [id]",
	Short: "Suspend (deactivate) a rack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		apiURL, _ := cmd.Flags().GetString("api-url")

		// Empty body for now
		resp, err := http.Post(
			fmt.Sprintf("%s/api/v1/racks/%s/suspend", apiURL, id),
			"application/json",
			nil,
		)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("suspend failed: %s", resp.Status)
		}

		fmt.Printf("Rack %s suspended\n", id)
		return nil
	},
}

var racksActivateCmd = &cobra.Command{
	Use:   "activate [id]",
	Short: "Activate a suspended rack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		apiURL, _ := cmd.Flags().GetString("api-url")

		resp, err := http.Post(
			fmt.Sprintf("%s/api/v1/racks/%s/activate", apiURL, id),
			"application/json",
			nil,
		)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("activate failed: %s", resp.Status)
		}

		fmt.Printf("Rack %s activated\n", id)
		return nil
	},
}

var racksRemoveCmd = &cobra.Command{
	Use:   "remove [id]",
	Short: "Remove a rack registry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		apiURL, _ := cmd.Flags().GetString("api-url")

		req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/v1/racks/%s", apiURL, id), nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to connect to mixer: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("remove failed: %s", resp.Status)
		}

		fmt.Printf("Rack %s removed\n", id)
		return nil
	},
}

func init() {
	adminCmd.AddCommand(adminRacksCmd)
	adminRacksCmd.AddCommand(racksListCmd)
	adminRacksCmd.AddCommand(racksApproveCmd)
	adminRacksCmd.AddCommand(racksRemoveCmd)
	adminRacksCmd.AddCommand(racksSuspendCmd)
	adminRacksCmd.AddCommand(racksActivateCmd)

	racksApproveCmd.Flags().String("name", "", "New name for the rack")
	// Mark flag required?
	// _ = racksApproveCmd.MarkFlagRequired("name") // Let's enforce in code for better error logic if needed
}
