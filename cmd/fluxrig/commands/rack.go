package commands

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/config"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/spf13/cobra"
)

var configFile string
var logger *slog.Logger

// rackCmd represents the rack command
var rackCmd = &cobra.Command{
	Use:   "rack",
	Short: "Starts the Rack Runtime (Hot Path)",
	Long:  `Initializes the fluxrig Rack, connects to the Bus (NATS), loads the Registry, and begins processing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 1. Setup Logging (Basic for startup)
		logger = loggerPkg.New(loggerPkg.Config{
			Level:     "info",
			Component: loggerPkg.TypeRack,
			Name:      "fluxrig-rack-init",
			Writer:    os.Stdout,
		})
		// logger.Info("Starting FluxRig Rack...") // Removed to avoid double print with main logger

		// 2. Load Configuration
		// We load config first to know WHERE to log.
		// If config load fails, we default to stdout/stderr.
		cfg, err := config.LoadRack(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// 3. Setup Standard Logger
		logger = loggerPkg.New(loggerPkg.Config{
			Level:     cfg.Logging.Level,
			Component: loggerPkg.TypeRack,
			Name:      cfg.Rack.Name,
			Writer:    os.Stdout, // CLI always to stdout, tests capture via redirection
		})
		slog.SetDefault(logger)

		logger.Info("Starting FluxRig Rack...", "version", "v0.1.0-alpha")

		// 3. Connect to Bus & Start Agent
		if err := RunAgent(cfg, logger); err != nil {
			logger.Error("Agent failed", "error", err)
			os.Exit(1)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(rackCmd)
	rackCmd.Flags().StringVarP(&configFile, "config", "c", "", "Path to fluxrig.toml")
}
