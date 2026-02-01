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

// @title FluxRig Mixer API
// @version 0.1.0
// @description Control Plane API for FluxRig Orchestration.
// @termsOfService http://swagger.io/terms/

// @contact.name JAAB Tech Support
// @contact.url https://jaab.tech
// @contact.email support@jaab.tech

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
	var scenarioPath string

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "-s", "--scenario":
			if i+1 < len(args) {
				scenarioPath = args[i+1]
				i++
			}
		}
	}

	if configPath == "" {
		if os.Getenv("FLUXRIG_CONFIG") != "" {
			configPath = os.Getenv("FLUXRIG_CONFIG")
		} else {
			configPath = "fluxrig-mixer.toml"
		}
	}

	// 2. Load Configuration (to get Log Level)
	cfg, err := config.LoadMixer(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
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
	if scenarioPath == "" && cfg.Mixer.StartupScenario != "" {
		scenarioPath = cfg.Mixer.StartupScenario
	}

	// 4. Run Application
	app := mixer.NewApp(cfg, scenarioPath)
	if err := app.Run(); err != nil {
		slog.Error("Mixer exited with error", "error", err)
		os.Exit(1)
	}
}
