// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// @title fluxrig Mixer API
// @version 0.0.0-dev
// @description Control Plane API for fluxrig Orchestration. The Mixer acts as the brain of the fleet, managing Rack enrollment, Scenario deployment, and global Telemetry aggregation.

// @contact.name JAAB Tech Support
// @contact.url https://fluxrig.org
// @contact.email fluxrig@jaab.tech

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8090
// @BasePath /api/v1
// @schemes http

// @externalDocs.description fluxrig Architecture Guide
// @externalDocs.url https://fluxrig.org/docs/architecture
package main

import (
	"os"

	"github.com/jaab-tech/fluxrig/cmd/fluxrig-mixer/mixercmd"
)

func main() {
	os.Exit(mixercmd.Run(os.Args[1:]))
}
