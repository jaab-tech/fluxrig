// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
	"github.com/jaab-tech/fluxrig/pkg/viz/likec4"
)

var scenarioVizCmd = &cobra.Command{
	Use:   "viz <scenario-file>",
	Short: "Generate an interactive architecture model (LikeC4) from a scenario",
	Long: `Generates a LikeC4 model (scenario.likec4) from a scenario YAML file, for
interactive drill-down visualization and topology validation.

The output is plain text; view it with the likec4 CLI (dev-time tooling):

  npx likec4 start <output-dir>

Broken wiring is rendered, not hidden: wires that reference undefined gears
appear as red placeholder elements, and every irregularity is reported.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		outDir, _ := cmd.Flags().GetString("out")

		safePath, err := path.Sanitize(args[0])
		if err != nil {
			return fmt.Errorf("invalid file path: %w", err)
		}
		content, err := os.ReadFile(safePath)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// The visualizer parses leniently so scenarios written for any
		// fluxrig version (or broken ones) can still be reviewed.
		sc, parseNotes, err := likec4.ParseScenario(content)
		if err != nil {
			return err
		}

		// The strict runtime schema check is advisory here: visualizing a
		// broken scenario is precisely how you find what is broken.
		var strict registry.Scenario
		if errParse := yaml.Unmarshal(content, &strict); errParse != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: strict schema parse: %v\n", errParse)
		} else if errVal := strict.Validate(); errVal != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: scenario validation: %v\n", errVal)
		}

		// Classify and link gears from their manifests; unknown types fall
		// back to name heuristics inside the generator.
		factory := gears.NewFactory()
		manifestFor := func(gearType string) (sdk.Manifest, bool) {
			return factory.Manifest(gearType)
		}

		out, problems, err := likec4.Generate(sc, &likec4.Source{
			Name: filepath.Base(safePath),
			YAML: content,
		}, manifestFor)
		if err != nil {
			return err
		}
		problems = append(parseNotes, problems...)

		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("failed to create output dir: %w", err)
		}
		target := filepath.Join(outDir, "scenario.likec4")
		if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
			return fmt.Errorf("failed to write model: %w", err)
		}

		fmt.Printf("Generated %s (%d gears, %d wires)\n", target, len(sc.Gears), len(sc.Wires))
		for _, p := range problems {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", p)
		}
		return nil
	},
}

func init() {
	scenarioVizCmd.Flags().StringP("out", "o", "build/likec4", "Output directory for the generated model")
	scenarioCmd.AddCommand(scenarioVizCmd)
}
