package commands

import (
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

		fmt.Printf("[CLI] Querying Mixer Config at %s...\n", apiURL)
		// 1. Get Mixer Config
		fmt.Println("--- Mixer Configuration ---")
		if err := showMixerConfig(apiURL); err != nil {
			fmt.Printf("Warning: failed to get mixer configuration: %v\n", err)
		}
		fmt.Println()

		fmt.Printf("[CLI] Querying Racks Config at %s...\n", apiURL)
		// 2. Get Racks Config
		fmt.Println("--- Racks Configuration ---")
		if err := showRacksConfig(apiURL); err != nil {
			fmt.Printf("Warning: failed to get racks configuration: %v\n", err)
		}

		return nil
	},
}

func showMixerConfig(apiURL string) error {
	resp, err := http.Get(apiURL + "/api/v1/config")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned error: %s", resp.Status)
	}

	body, _ := io.ReadAll(resp.Body)
	var config any
	if err := json.Unmarshal(body, &config); err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(config)
}

func showRacksConfig(apiURL string) error {
	resp, err := http.Get(apiURL + "/api/v1/racks")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned error: %s", resp.Status)
	}

	var racks []struct {
		MachineID uint16         `json:"machine_id"`
		Name      string         `json:"name"`
		Status    string         `json:"status"`
		Config    map[string]any `json:"config"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&racks); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCONFIGURATION")
	for _, r := range racks {
		configStr := "no-config"
		if len(r.Config) > 0 {
			c, _ := json.Marshal(r.Config)
			configStr = string(c)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", r.MachineID, r.Name, r.Status, configStr)
	}
	w.Flush()
	return nil
}

func init() {
	configCmd.Flags().String("api-url", "", "Mixer API URL (overrides FLUXRIG_API_URL)")
	rootCmd.AddCommand(configCmd)
}
