package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// racksCmd represents the racks command (read-only view)
var racksCmd = &cobra.Command{
	Use:   "racks",
	Short: "List all registered racks",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL, _ := cmd.Flags().GetString("api-url")
		resp, err := http.Get(apiURL + "/api/v1/racks")
		if err != nil {
			return fmt.Errorf("failed to connect to mixer: %w", err)
		}
		defer resp.Body.Close()

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
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tIP\tPORT\tLAST SEEN")
		for _, r := range racks {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\n", r.MachineID, r.Name, r.Status, r.IP, r.Port, r.LastSeen)
		}
		w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(racksCmd)
	racksCmd.Flags().String("api-url", "http://localhost:8090", "URL of the Mixer API")
}
