// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/gears"
)

var gearsCmd = &cobra.Command{
	Use:   "gears",
	Short: "Inspect the gears this binary can run",
}

var gearsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the gear types this build can construct",
	RunE: func(cmd *cobra.Command, _ []string) error {
		f := gears.NewFactory()
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "TYPE\tCATEGORY\tSTATUS\tSUMMARY")
		for _, m := range f.Manifests() {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Type, m.Category, m.Status, m.Summary)
		}
		return w.Flush()
	},
}

var gearsManifestCmd = &cobra.Command{
	Use:   "manifest [type]",
	Short: "Print a gear manifest (identity, ports, config schema) as JSON",
	Long: `Print the machine-readable manifest for a gear type: identity, category,
declared ports, and configuration JSON Schema. With --all, prints the whole
catalog. This is the source of truth for scenario validation and tooling.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f := gears.NewFactory()
		all, _ := cmd.Flags().GetBool("all")

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")

		if all {
			return enc.Encode(f.Manifests())
		}
		if len(args) != 1 {
			return fmt.Errorf("provide a gear type, or --all (see 'fluxrig gears list')")
		}
		m, ok := f.Manifest(args[0])
		if !ok {
			return fmt.Errorf("unknown gear type %q (see 'fluxrig gears list')", args[0])
		}
		return enc.Encode(m)
	},
}

func init() {
	gearsManifestCmd.Flags().Bool("all", false, "Print the manifest catalog for every gear type")
	gearsCmd.AddCommand(gearsListCmd)
	gearsCmd.AddCommand(gearsManifestCmd)
	rootCmd.AddCommand(gearsCmd)
}
