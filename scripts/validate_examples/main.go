// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run validate_examples.go <path_to_example>")
		os.Exit(1)
	}

	path := os.Args[1]
	fmt.Printf("Validating example: %s\n", path)

	if strings.Contains(path, "mixer") {
		cfg, err := config.LoadMixer(path)
		if err != nil {
			fmt.Printf("FAIL: LoadMixer error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("PASS: Mixer config loaded successfully (Name: %s)\n", cfg.Base.Name)
	} else {
		cfg, err := config.LoadRack(path)
		if err != nil {
			fmt.Printf("FAIL: LoadRack error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("PASS: Rack config loaded successfully (Name: %s)\n", cfg.Base.Name)
	}
}
