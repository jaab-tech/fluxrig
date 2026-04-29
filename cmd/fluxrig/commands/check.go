// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
)

var configPathCheck string

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Perform a pre-flight diagnostic check of the environment",
	Long: `Verifies connectivity to the NATS bus, Mixer API, and local storage integrity.
Essential for troubleshooting connectivity and permission issues before production launch.`,
	Run: func(cmd *cobra.Command, args []string) {
		if configPathCheck != "" {
			// Try loading as Rack config
			if cfg, err := config.LoadRack(configPathCheck); err == nil {
				_ = os.Setenv("FLUXRIG_BUS_URL", cfg.Rack.Bus.URL)
				if cfg.Store.Dir != "" {
					_ = os.Setenv("FLUXRIG_STORE_DIR", cfg.Store.Dir)
				}
			}
			// Also try loading as Mixer config to get API port if available
			if cfg, err := config.LoadMixer(configPathCheck); err == nil {
				_ = os.Setenv("FLUXRIG_API_URL", fmt.Sprintf("http://localhost:%d", cfg.API.Port))
			}
		}

		fmt.Println(color.CyanString("FluxRig Pre-flight Check"))
		fmt.Println(color.CyanString("========================="))

		allOk := true

		// 1. NATS Check
		if !checkNATS() {
			allOk = false
		}

		// 2. Mixer API Check
		if !checkAPI() {
			allOk = false
		}

		// 3. Store Check
		storeDir, _ := cmd.Flags().GetString("store-dir")
		if !checkStore(storeDir) {
			allOk = false
		}

		// 4. PKI Check
		if !checkPKI() {
			allOk = false
		}

		fmt.Println()
		if allOk {
			fmt.Println(color.GreenString("✓ All systems operational."))
		} else {
			fmt.Println(color.RedString("✗ Some checks failed. Please review the errors above."))
			os.Exit(1)
		}
	},
}

func checkNATS() bool {
	fmt.Printf("%-25s ", "NATS Connectivity")
	url := os.Getenv("FLUXRIG_BUS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}

	b := bus.NewNatsBus("flux-check")
	err := b.Connect(url, bus.ConnectOptions{
		Name:           "flux-check",
		ConnectTimeout: 2 * time.Second,
	})

	if err != nil {
		fmt.Printf("[%s] (%s) %v\n", color.RedString("FAIL"), url, err)
		return false
	}
	defer b.Close()

	fmt.Printf("[%s] (%s)\n", color.GreenString("OK"), url)
	return true
}

func checkAPI() bool {
	fmt.Printf("%-25s ", "Mixer API (8090)")
	url := os.Getenv("FLUXRIG_API_URL")
	if url == "" {
		url = "http://localhost:8090"
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", url+"/api/v1/health", nil)
	if err != nil {
		fmt.Printf("[%s] (%s) %v\n", color.RedString("FAIL"), url, err)
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[%s] (%s) %v\n", color.RedString("FAIL"), url, err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[%s] (%s) Status: %d\n", color.RedString("FAIL"), url, resp.StatusCode)
		return false
	}

	fmt.Printf("[%s] (%s)\n", color.GreenString("OK"), url)
	return true
}

func checkStore(overridePath string) bool {
	fmt.Printf("%-25s ", "Local CAS Store")
	path := os.Getenv("FLUXRIG_STORE_DIR")
	if path == "" {
		path = overridePath
	}
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".fluxrig", "store")
	}

	// Check if exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		fmt.Printf("[%s] Missing (%s)\n", color.YellowString("WARN"), path)
		return true // Warning is not a failure if it's the first run
	}

	if err != nil {
		fmt.Printf("[%s] %v\n", color.RedString("FAIL"), err)
		return false
	}

	if !info.IsDir() {
		fmt.Printf("[%s] Path is not a directory (%s)\n", color.RedString("FAIL"), path)
		return false
	}

	// Try writing a small file
	testFile := filepath.Join(path, ".check_tmp")
	if err := os.WriteFile(testFile, []byte("ok"), 0600); err != nil {
		fmt.Printf("[%s] Write permission denied (%s)\n", color.RedString("FAIL"), path)
		return false
	}
	_ = os.Remove(testFile)

	fmt.Printf("[%s] (%s)\n", color.GreenString("OK"), path)
	return true
}

func checkPKI() bool {
	fmt.Printf("%-25s ", "PKI/TLS Certificates")
	home, _ := os.UserHomeDir()
	pkiDir := filepath.Join(home, ".fluxrig", "pki")

	requiredFiles := []string{"cluster.key", "machine.key"}
	missing := 0
	for _, f := range requiredFiles {
		path := filepath.Join(pkiDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing++
		}
	}

	if missing == len(requiredFiles) {
		fmt.Printf("[%s] No keys found (Bootstrap required)\n", color.YellowString("WARN"))
		return true
	}

	if missing > 0 {
		fmt.Printf("[%s] Partial keys found in %s\n", color.RedString("FAIL"), pkiDir)
		return false
	}

	fmt.Printf("[%s] Keys present in %s\n", color.GreenString("OK"), pkiDir)
	return true
}

func init() {
	checkCmd.Flags().StringVarP(&configPathCheck, "config", "c", "", "Path to configuration file")
	rootCmd.AddCommand(checkCmd)
}
