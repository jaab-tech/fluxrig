// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// @title FluxRig Mixer API
// @version 0.0.0-dev
// @description Control Plane API for FluxRig Orchestration.

// @contact.name JAAB Tech Support
// @contact.url https://fluxrig.org
// @contact.email fluxrig@jaab.tech

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8090
// @BasePath /api/v1
// @schemes http
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/config"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/mixer"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

func main() {
	// 1. Parse Flags
	var configPath string
	var scenarioRef string

	var explicitConfig bool
	var autoAdopt bool
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Printf("Usage of fluxrig-mixer:\n")
			fmt.Printf("  -c, --config string    Path to fluxrig-mixer.toml\n")
			fmt.Printf("  -s, --scenario string  Path to a scenario file to load on startup\n")
			fmt.Printf("      --auto-adopt       Automatically adopt connecting Racks (Dev only)\n")
			fmt.Printf("  -h, --help             Show this help message\n")
			os.Exit(0)
		case "-c", "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				explicitConfig = true
				i++
			}
		case "-s", "--scenario":
			if i+1 < len(args) {
				scenarioRef = args[i+1]
				i++
			}
		case "--auto-adopt":
			autoAdopt = true
		default:
			// Capture first positional argument as scenario reference
			if scenarioRef == "" && args[i] != "" && args[i][0] != '-' {
				scenarioRef = args[i]
			}
		}
	}

	if configPath == "" {
		if os.Getenv("FLUXRIG_CONFIG") != "" {
			configPath = os.Getenv("FLUXRIG_CONFIG")
			explicitConfig = true
		} else {
			// Zero-Config: try default file, but don't fail if it's missing
			defaultPath := "fluxrig-mixer.toml"
			if _, err := os.Stat(defaultPath); err == nil {
				configPath = defaultPath
			}
		}
	}

	// 2. Load Configuration (to get Log Level)
	cfg, err := config.LoadMixer(configPath)
	if err != nil {
		if explicitConfig {
			fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
			os.Exit(1)
		}
		// This should not be reachable if existence check above works,
		// but kept as safety.
		fmt.Fprintf(os.Stderr, "failed to load default config: %v\n", err)
		os.Exit(1)
	}

	if autoAdopt {
		cfg.Enrollment.AutoAdopt = true
		slog.Warn("AUTO-ADOPT ENABLED via CLI flag")
	}

	// 3. Setup Standard Logger
	logger := loggerPkg.New(loggerPkg.Config{
		Level:      cfg.Logging.Level,
		EntityType: loggerPkg.TypeMixer,
		Name:       "fluxrig-mixer",
		Writer:     os.Stdout,
	})

	// Buffer pre-telemetry logs for 1-to-1 parity
	bufHandler := telemetry.NewBufferHandler(logger.Handler())
	bufLogger := slog.New(bufHandler)
	slog.SetDefault(bufLogger)

	// Fallback to Config
	if scenarioRef == "" && cfg.Mixer.StartupScenario != "" {
		scenarioRef = cfg.Mixer.StartupScenario
	}

	// 4. Run Application
	app := mixer.NewApp(cfg, scenarioRef)
	if err := app.Run(); err != nil {
		slog.Error("Mixer exited with error", "error", err)
		os.Exit(1)
	}
}
