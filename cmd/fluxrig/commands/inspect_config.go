// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "configuration",
	Short: "Show runtime configuration for Mixers and Racks",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL, _ := cmd.Flags().GetString("api-url")
		if apiURL == "" {
			apiURL = os.Getenv("FLUXRIG_API_URL")
		}
		if apiURL == "" {
			apiURL = "http://localhost:8090"
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[CLI] Querying Mixer Config at %s...\n", apiURL)
		// 1. Get Mixer Config
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "--- Mixer Configuration ---")
		if err := showMixerConfig(cmd.Context(), apiURL, cmd.OutOrStdout()); err != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Warning: failed to get mixer configuration: %v\n", err)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[CLI] Querying Racks Config at %s...\n", apiURL)
		// 2. Get Racks Config
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "--- Racks Configuration ---")
		if err := showRacksConfig(cmd.Context(), apiURL, cmd.OutOrStdout()); err != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Warning: failed to get racks configuration: %v\n", err)
		}

		return nil
	},
}

func showMixerConfig(ctx context.Context, apiURL string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL+"/api/v1/config", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned error: %s", resp.Status)
	}

	body, _ := io.ReadAll(resp.Body)
	var config any
	if err := json.Unmarshal(body, &config); err != nil {
		return err
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(config)
}

func showRacksConfig(ctx context.Context, apiURL string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL+"/api/v1/racks", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned error: %s", resp.Status)
	}

	var racks []struct {
		Name      string         `json:"name"`
		Status    string         `json:"status"`
		Config    map[string]any `json:"config"`
		MachineID uint16         `json:"machine_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&racks); err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCONFIGURATION")
	for _, r := range racks {
		configStr := "no-config"
		if len(r.Config) > 0 {
			c, _ := json.Marshal(r.Config)
			configStr = string(c)
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", r.MachineID, r.Name, r.Status, configStr)
	}
	_ = w.Flush()
	return nil
}

func init() {
	configCmd.Flags().String("api-url", "", "Mixer API URL (overrides FLUXRIG_API_URL)")
	rootCmd.AddCommand(configCmd)
}
