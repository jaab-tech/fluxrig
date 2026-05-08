// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"github.com/spf13/cobra"
)

var apiURL string

// adminCmd represents the admin command
var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Administrative commands for fluxrig Mixer",
	Long:  `Perform management operations on the fluxrig Mixer via its API.`,
}

func init() {
	rootCmd.AddCommand(adminCmd)
	adminCmd.PersistentFlags().StringVar(&apiURL, "api-url", "http://localhost:8090", "URL of the Mixer API")
}
