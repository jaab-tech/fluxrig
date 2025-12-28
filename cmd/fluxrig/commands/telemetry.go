package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// logsCmd represents the logs command
var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View recent telemetry logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		apiURL, _ := cmd.Flags().GetString("api-url")
		minLevel, _ := cmd.Flags().GetString("min-level")
		entity, _ := cmd.Flags().GetString("entity")
		since, _ := cmd.Flags().GetString("since")
		until, _ := cmd.Flags().GetString("until")

		params := url.Values{}
		params.Set("limit", fmt.Sprintf("%d", limit))
		if minLevel != "" {
			params.Set("min_level", minLevel)
		}
		if entity != "" {
			params.Set("entity", entity)
		}
		if since != "" {
			params.Set("since", since)
		}
		if until != "" {
			params.Set("until", until)
		}

		resp, err := http.Get(fmt.Sprintf("%s/api/v1/telemetry/logs?%s", apiURL, params.Encode()))
		if err != nil {
			return fmt.Errorf("failed to connect to mixer: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned error: %s", resp.Status)
		}

		type LogEntry struct {
			Timestamp  time.Time      `json:"timestamp"`
			EntityName string         `json:"entity_name"`
			Severity   string         `json:"severity"`
			Body       string         `json:"body"`
			Attributes map[string]any `json:"attributes"`
		}

		var logs []LogEntry
		if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tENTITY\tLEVEL\tMESSAGE")
		for _, l := range logs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.Timestamp.Format(time.TimeOnly), l.EntityName, l.Severity, l.Body)
		}
		w.Flush()
		return nil
	},
}

// metricsCmd represents the metrics command
var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "View recent telemetry metrics",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		apiURL, _ := cmd.Flags().GetString("api-url")
		name, _ := cmd.Flags().GetString("name")
		entity, _ := cmd.Flags().GetString("entity")
		since, _ := cmd.Flags().GetString("since")
		until, _ := cmd.Flags().GetString("until")

		params := url.Values{}
		params.Set("limit", fmt.Sprintf("%d", limit))
		if name != "" {
			params.Set("name", name)
		}
		if entity != "" {
			params.Set("entity", entity)
		}
		if since != "" {
			params.Set("since", since)
		}
		if until != "" {
			params.Set("until", until)
		}

		resp, err := http.Get(fmt.Sprintf("%s/api/v1/telemetry/metrics?%s", apiURL, params.Encode()))
		if err != nil {
			return fmt.Errorf("failed to connect to mixer: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned error: %s", resp.Status)
		}

		type MetricEntry struct {
			Timestamp  time.Time      `json:"timestamp"`
			EntityName string         `json:"entity_name"`
			Name       string         `json:"name"`
			Type       string         `json:"type"`
			Value      float64        `json:"value"`
			Attributes map[string]any `json:"attributes"`
		}

		var metrics []MetricEntry
		if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tENTITY\tNAME\tTYPE\tVALUE")
		for _, m := range metrics {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.2f\n", m.Timestamp.Format(time.TimeOnly), m.EntityName, m.Name, m.Type, m.Value)
		}
		w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(metricsCmd)

	// Logs flags
	logsCmd.Flags().Int("limit", 50, "Number of logs to retrieve")
	logsCmd.Flags().String("api-url", "http://localhost:8090", "URL of the Mixer API")
	logsCmd.Flags().String("min-level", "", "Minimum log level (DEBUG, INFO, WARN, ERROR)")
	logsCmd.Flags().String("entity", "", "Filter by entity name")
	logsCmd.Flags().String("since", "", "Start time (duration like 1h or ISO timestamp)")
	logsCmd.Flags().String("until", "", "End time (ISO timestamp)")

	// Metrics flags
	metricsCmd.Flags().Int("limit", 50, "Number of metrics to retrieve")
	metricsCmd.Flags().String("api-url", "http://localhost:8090", "URL of the Mixer API")
	metricsCmd.Flags().String("name", "", "Filter by metric name")
	metricsCmd.Flags().String("entity", "", "Filter by entity name")
	metricsCmd.Flags().String("since", "", "Start time (duration like 1h or ISO timestamp)")
	metricsCmd.Flags().String("until", "", "End time (ISO timestamp)")
}
