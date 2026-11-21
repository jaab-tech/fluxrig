// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/runtime"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
)

var scenarioPermissionsCmd = &cobra.Command{
	Use:   "permissions <scenario-file>...",
	Short: "Write the NATS permissions of each Rack of one or more scenarios",
	Long: `Writes one NATS user for each Rack, limited to the subjects that Rack uses
to run the scenario: its own messages and events, the wires it consumes, the
events its gears read and the buckets they use. Paste the users into the
authorization block of an external NATS, and generate them again whenever the
scenario changes: a Rack that lacks a permission logs the refusal as an error.

The Racks are the ones the gears are deployed to. A gear that names no Rack
runs on every Rack, so a scenario with one needs --rack for each Rack.

With several scenarios, each Rack gets what all of them need, so it can run
any one of them without new permissions.

--user is the NATS user of a Rack, with {rack} for its name. With TLS
verify_and_map, it is the subject of the Rack's certificate.`,
	Example: `  fluxrig scenario permissions scenario.yaml --rack rack-a --rack rack-b \
    --user 'CN={rack},O=example' --config rack.toml`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		racks, _ := cmd.Flags().GetStringSlice("rack")
		user, _ := cmd.Flags().GetString("user")
		cfgPath, _ := cmd.Flags().GetString("config")

		scenarios := make([]*registry.Scenario, 0, len(args))
		for _, arg := range args {
			sc, err := readPermissionScenario(arg)
			if err != nil {
				return err
			}
			scenarios = append(scenarios, sc)
		}
		// The subjects a Rack is configured with: its config file, or the defaults.
		cfg, err := config.LoadRack(cfgPath)
		if err != nil {
			return fmt.Errorf("failed to load rack config: %w", err)
		}
		names := runtime.PermissionSubjects{Stream: cfg.Snake.StreamName, Telemetry: cfg.Telemetry.BaseSubject, TelemetryStream: cfg.Telemetry.StreamName}
		if cfg.Snake.DLQEnabled {
			names.DLQ = cfg.Snake.DLQSubject
		}
		if racks, err = permissionRacks(scenarios, racks); err != nil {
			return err
		}
		factory := gears.NewFactory()
		return writePermissions(cmd.OutOrStdout(), scenarios, racks, user, names, factory.Manifest)
	},
}

// readPermissionScenario reads and validates one scenario file.
func readPermissionScenario(file string) (*registry.Scenario, error) {
	safePath, err := path.Sanitize(file)
	if err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}
	content, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	var sc registry.Scenario
	if err = yaml.Unmarshal(content, &sc); err != nil {
		return nil, fmt.Errorf("failed to parse scenario %s: %w", file, err)
	}
	if err = sc.Validate(); err != nil {
		return nil, fmt.Errorf("invalid scenario %s: %w", file, err)
	}
	return &sc, nil
}

// permissionRacks returns the Racks to write users for: the ones given, or the
// ones the gears are deployed to.
func permissionRacks(scenarios []*registry.Scenario, given []string) ([]string, error) {
	if len(given) > 0 {
		return given, nil
	}
	seen := map[string]bool{}
	var everywhere []string
	for _, sc := range scenarios {
		for _, g := range sc.Gears {
			if target, _ := g.Deploy.(string); target != "" {
				seen[target] = true
			} else {
				everywhere = append(everywhere, g.Name)
			}
		}
	}
	if len(everywhere) > 0 {
		return nil, fmt.Errorf("gears %s name no Rack and run on every Rack: name the Racks with --rack", strings.Join(everywhere, ", "))
	}
	racks := make([]string, 0, len(seen))
	for r := range seen {
		racks = append(racks, r)
	}
	sort.Strings(racks)
	return racks, nil
}

func writePermissions(w io.Writer, scenarios []*registry.Scenario, racks []string, user string, names runtime.PermissionSubjects, manifest runtime.ManifestLookup) error {
	list := func(subjects []string) string {
		quoted := make([]string, len(subjects))
		for i, s := range subjects {
			quoted[i] = "      " + strconv.Quote(s)
		}
		return "[\n" + strings.Join(quoted, ",\n") + "\n    ]"
	}
	titles := make([]string, len(scenarios))
	for i, sc := range scenarios {
		titles[i] = sc.Meta.Name + " " + sc.Meta.Version
	}
	if _, err := fmt.Fprintf(w, "# NATS users for %s, one for each Rack.\n# Generate again when a scenario changes.\n", strings.Join(titles, ", ")); err != nil {
		return err
	}
	for _, rack := range racks {
		p := unionPermissions(scenarios, rack, names, manifest)
		_, err := fmt.Fprintf(w, "{ user: %s, permissions: {\n    publish: { allow: %s }\n    subscribe: { allow: %s }\n    allow_responses: true\n} }\n",
			strconv.Quote(strings.ReplaceAll(user, "{rack}", rack)), list(p.Publish), list(p.Subscribe))
		if err != nil {
			return err
		}
	}
	return nil
}

// unionPermissions returns what rack needs to run any one of scenarios.
func unionPermissions(scenarios []*registry.Scenario, rack string, names runtime.PermissionSubjects, manifest runtime.ManifestLookup) runtime.Permissions {
	pub, sub := map[string]bool{}, map[string]bool{}
	for _, sc := range scenarios {
		p := runtime.RackPermissions(sc, rack, names, manifest)
		for _, s := range p.Publish {
			pub[s] = true
		}
		for _, s := range p.Subscribe {
			sub[s] = true
		}
	}
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return runtime.Permissions{Publish: keys(pub), Subscribe: keys(sub)}
}

func init() {
	scenarioPermissionsCmd.Flags().StringSlice("rack", nil, "Rack to write a user for (repeatable); default: the Racks the gears are deployed to")
	scenarioPermissionsCmd.Flags().String("user", "{rack}", "NATS user of a Rack, with {rack} for its name")
	scenarioPermissionsCmd.Flags().StringP("config", "c", "", "Rack config file the subject names are read from; default: the Rack defaults")
	scenarioCmd.AddCommand(scenarioPermissionsCmd)
}
