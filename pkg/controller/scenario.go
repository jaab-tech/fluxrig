// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// ScenarioPublisher defines the interface for publishing scenarios to racks.
type ScenarioPublisher interface {
	Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error
}

// ScenarioManager defines the interface for managing scenarios.
type ScenarioManager interface {
	Import(ctx context.Context, content []byte, dryRun bool) (string, error)
	Activate(ctx context.Context, name string) error
	CurrentVersion() string
	CurrentName() string
	GetActiveScenario() *registry.Scenario
}

// ScenarioController manages the lifecycle of the Active Scenario.
type ScenarioController struct {
	log         *slog.Logger
	dataPath    string // Path to the data directory (e.g. ./data)
	repoPath    string // Path to the scenarios git repository (dataPath/scenarios)
	store       *duckdb.Store
	idGen       *idgen.IDGenerator
	bus         ScenarioPublisher // For pushing scenarios to racks
	mu          sync.Mutex
	active      *registry.Scenario // Currently active scenario
	repoReady   bool               // Whether the scenarios repo has been initialized
	mixerID     uuid.UUID
	waitTimeout time.Duration
}

func NewScenarioController(log *slog.Logger, dataPath string, store *duckdb.Store, ig *idgen.IDGenerator, mixerID uuid.UUID, waitTimeout time.Duration) *ScenarioController {
	repoPath := filepath.Join(dataPath, "scenarios")
	return &ScenarioController{
		log:         log.With("flux.type", "SCENARIO", "flux.name", "scenario-ctrl"),
		dataPath:    dataPath,
		repoPath:    repoPath,
		store:       store,
		idGen:       ig,
		mixerID:     mixerID,
		waitTimeout: waitTimeout,
	}
}

// SetBus sets the bus for publishing scenarios to racks.
func (c *ScenarioController) SetBus(bus ScenarioPublisher) {
	c.bus = bus
}

// CurrentVersion returns the version of the currently active scenario.
func (c *ScenarioController) CurrentVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return c.active.Meta.Version
	}
	return "none"
}

// CurrentName returns the name of the currently active scenario.
func (c *ScenarioController) CurrentName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return c.active.Meta.Name
	}
	return "none"
}

// GetActiveScenario returns the currently active scenario.
func (c *ScenarioController) GetActiveScenario() *registry.Scenario {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// Import validates and persists a scenario to the repository.
// It returns the sanitized scenario name and any error.
func (c *ScenarioController) Import(ctx context.Context, content []byte, dryRun bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.log.Info("importing scenario", "dry_run", dryRun, "size", len(content))

	// 1. Parse YAML
	var s registry.Scenario
	if err := yaml.Unmarshal(content, &s); err != nil {
		return "", fmt.Errorf("yaml parse error: %w", err)
	}

	// 2. Validate (Logic Gate)
	if err := s.Validate(); err != nil {
		return "", fmt.Errorf("validation failed: %w", err)
	}

	if dryRun {
		c.log.Info("dry-run successful")
		return "", nil
	}

	// 3. Ensure scenarios git repo exists
	if err := c.ensureScenariosRepo(); err != nil {
		c.log.Warn("failed to ensure scenarios repo", "error", err)
	}

	// 4. Persist to Disk & Git Commit
	// Save scenario by name: data/scenarios/{name}.yaml
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version)
	}
	safeName := c.sanitizeName(scenarioName)

	scenarioFile := filepath.Join(c.repoPath, safeName+".yaml")
	if err := os.WriteFile(scenarioFile, content, 0600); err != nil {
		return safeName, fmt.Errorf("failed to write scenario file: %w", err)
	}

	c.log.Info("scenario imported", "name", safeName, "version", s.Meta.Version)
	return safeName, nil
}

// Activate loads a scenario by name and pushes it to racks.
// Use this to switch between stored scenarios.
// PushActiveToRack pushes the currently active scenario to a specific rack.
func (c *ScenarioController) PushActiveToRack(ctx context.Context, rackName string) error {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()

	if active == nil {
		return nil // No active scenario to push
	}

	if c.bus == nil {
		return fmt.Errorf("scenario bus not initialized")
	}

	// We need the original content or we re-marshal the active structure
	content, err := yaml.Marshal(active)
	if err != nil {
		return fmt.Errorf("failed to marshal active scenario: %w", err)
	}

	return c.pushScenarioToRacks(ctx, active, content, rackName)
}

func (c *ScenarioController) Activate(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	safeName := c.sanitizeName(name)
	scenarioFile := filepath.Join(c.repoPath, safeName+".yaml")

	// 1. Load scenario from disk
	// Clean the path to avoid G304
	scenarioFile = filepath.Clean(scenarioFile)
	content, err := os.ReadFile(scenarioFile)
	if err != nil {
		return fmt.Errorf("scenario not found: %s", safeName)
	}

	var s registry.Scenario
	if err := yaml.Unmarshal(content, &s); err != nil {
		return fmt.Errorf("failed to parse scenario: %w", err)
	}

	// 2. Update active pointer
	activeFile := filepath.Join(c.repoPath, "active")
	if err := os.WriteFile(activeFile, []byte(safeName), 0600); err != nil {
		c.log.Warn("failed to write active scenario pointer", "error", err)
	}

	// 3. Set as active scenario in memory
	c.active = &s

	// 4. Register entities in DuckDB (if store available)
	if c.store != nil && c.idGen != nil {
		// Clean up existing scenario entities (gears, ports, wires, scenarios) to avoid name conflicts
		if err := c.store.ClearScenarioEntities(ctx); err != nil {
			c.log.Warn("failed to clear existing scenario entities", "error", err)
		}

		if err := c.registerScenarioEntities(ctx, &s); err != nil {
			c.log.Warn("failed to register scenario entities", "error", err)
		}
	}

	// 5. Push scenario to connected racks via NATS
	if c.bus != nil {
		if err := c.pushScenarioToRacks(ctx, &s, content, ""); err != nil {
			c.log.Warn("failed to push scenario to racks", "error", err)
		}
	}

	c.log.Info("scenario activated", "name", safeName, "version", s.Meta.Version)
	return nil
}

// List returns all available scenario names in the repository.
func (c *ScenarioController) List() ([]string, error) {
	entries, err := os.ReadDir(c.repoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var scenarios []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") && name != "active" {
			scenarios = append(scenarios, strings.TrimSuffix(name, ".yaml"))
		}
	}
	return scenarios, nil
}

// GetActiveName returns the name of the currently active scenario.
func (c *ScenarioController) GetActiveName() string {
	activeFile := filepath.Join(c.repoPath, "active")
	activeFile = filepath.Clean(activeFile)
	data, err := os.ReadFile(activeFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// sanitizeName makes a scenario name safe for filesystem use.
// It removes any characters that could be used for path traversal.
func (c *ScenarioController) sanitizeName(name string) string {
	// 1. Remove any path-related characters
	safeName := strings.ReplaceAll(name, "..", "")
	safeName = strings.ReplaceAll(safeName, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")

	// 2. Restrict to alphanumeric, hyphens, and underscores
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-_]`)
	safeName = reg.ReplaceAllString(safeName, "_")

	// 3. Trim and ensure not empty
	safeName = strings.Trim(safeName, "_-")
	if safeName == "" {
		safeName = "unnamed_scenario"
	}

	return safeName
}

// registerScenarioEntities persists scenario, gears, and wires to the registry.
// It also activates any racks defined in the scenario.
func (c *ScenarioController) registerScenarioEntities(ctx context.Context, s *registry.Scenario) error {
	// Register Scenario
	scenarioEID := c.idGen.NextEntityID(idgen.EntityScenario)
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version) // Fallback if no name
	}
	if err := c.store.RegisterScenario(ctx, scenarioEID, scenarioName, s.Meta.Version, len(s.Gears), len(s.Wires), c.mixerID); err != nil {
		return fmt.Errorf("failed to register scenario: %w", err)
	}
	c.log.Info("registered scenario", "name", scenarioName, "eid", scenarioEID)

	// Build a map of rack name -> machineID and activate racks defined in scenario
	rackMachineIDs := make(map[string]uuid.UUID)
	for _, rack := range s.Racks {
		if rack.Name != "" {
			// Activate the rack (promote from pending to active)
			if err := c.store.ActivateRack(ctx, rack.Name); err != nil {
				c.log.Warn("failed to activate rack", "name", rack.Name, "error", err)
			} else {
				c.log.Info("activated rack", "name", rack.Name)
			}

			// Get the rack's machine_id with a short retry to handle bootstrap races
			machineID, err := c.waitForRack(ctx, rack.Name)
			if err != nil {
				return fmt.Errorf("failed to register scenario: rack %s not found: %w", rack.Name, err)
			}
			rackMachineIDs[rack.Name] = machineID
		}
	}

	// Register Gears with their rack's machineID
	// Track generated port IDs for wire registration: gearName -> portSuffix -> ID
	gearPortIDs := make(map[string]map[string]uuid.UUID)

	for i := range s.Gears {
		g := &s.Gears[i] // Pointer to modify

		// Idempotent Gear Registration: Reuse ID if name already exists
		gearEID, err := c.store.GetEntityIDByName(ctx, g.Name)
		if err != nil {
			gearEID = c.idGen.NextEntityID(idgen.EntityGear)
		}
		g.ID = gearEID // Assign ID to spec

		mode := ""
		bind := ""
		connect := ""
		if cfg := g.Config; cfg != nil {
			if m, ok := cfg["mode"].(string); ok {
				mode = m
			}
			if b, ok := cfg["bind"].(string); ok {
				bind = b
			}
			if cn, ok := cfg["connect"].(string); ok {
				connect = cn
			}
		}

		// Get the machineID from the gear's deploy target
		var machineID uuid.UUID
		if deployTarget, ok := g.Deploy.(string); ok {
			if mid, exists := rackMachineIDs[deployTarget]; exists {
				machineID = mid
			}
		}

		// Phase 3: Register Implicit Ports (In/Out)
		// Input Port
		inPortName := g.Name + ".in"
		inPortID, err := c.store.GetEntityIDByName(ctx, inPortName)
		if err != nil {
			inPortID = c.idGen.NextEntityID(idgen.EntityPortInput)
		}

		if errReg := c.store.RegisterPort(ctx, inPortID, inPortName, uint16(idgen.EntityPortInput), gearEID, scenarioEID, machineID, c.mixerID); errReg != nil {
			c.log.Warn("failed to register input port", "name", inPortName, "error", errReg)
		}

		// Output Port
		outPortName := g.Name + ".out"
		outPortID, err := c.store.GetEntityIDByName(ctx, outPortName)
		if err != nil {
			outPortID = c.idGen.NextEntityID(idgen.EntityPortOutput)
		}

		if err := c.store.RegisterPort(ctx, outPortID, outPortName, uint16(idgen.EntityPortOutput), gearEID, scenarioEID, machineID, c.mixerID); err != nil {
			c.log.Warn("failed to register output port", "name", outPortName, "error", err)
		}

		// Store Port IDs for Gear and Wire registration
		ports := map[string]uuid.UUID{
			"in":  inPortID,
			"out": outPortID,
		}
		g.Ports = ports // Assign to spec for Rack transmission
		gearPortIDs[g.Name] = ports

		if err := c.store.RegisterGear(ctx, gearEID, g.Name, g.Type, mode, bind, connect, scenarioEID, machineID, ports, c.mixerID); err != nil {
			c.log.Warn("failed to register gear", "name", g.Name, "error", err)
		} else {
			c.log.Info("registered gear", "name", g.Name, "eid", gearEID, "machine_id", machineID)
		}
	}

	// Register Wires - use the machineID of the source gear's rack
	for i := range s.Wires {
		w := &s.Wires[i] // Pointer to modify
		wireEID := c.idGen.NextEntityID(idgen.EntityWire)
		w.ID = wireEID // Assign ID to spec

		// Resolve From/To to Port IDs
		resolvePortID := func(endpoint string) uuid.UUID {
			// Expected format: gearName.portSuffix (e.g., "gateway.out")
			lastDot := strings.LastIndex(endpoint, ".")
			if lastDot == -1 {
				return uuid.Nil
			}
			gearName := endpoint[:lastDot]
			portSuffix := endpoint[lastDot+1:]
			if p, ok := gearPortIDs[gearName]; ok {
				return p[portSuffix]
			}
			return uuid.Nil
		}

		fromID := resolvePortID(w.From)
		toID := resolvePortID(w.To)

		// Determine MachineID (Source Rack)
		var machineID uuid.UUID
		// Parse source gear name from wire.From (e.g., "gateway.out" -> "gateway")
		sourceGear := w.From
		lastDot := strings.LastIndex(sourceGear, ".")
		if lastDot != -1 {
			sourceGear = sourceGear[:lastDot]
		}

		// Find which rack this gear is deployed on
		for _, g := range s.Gears {
			if g.Name == sourceGear {
				if deployTarget, ok := g.Deploy.(string); ok {
					if mid, exists := rackMachineIDs[deployTarget]; exists {
						machineID = mid
					}
				}
				break
			}
		}

		if err := c.store.RegisterWire(ctx, wireEID, w.From, w.To, fromID, toID, scenarioEID, machineID, c.mixerID); err != nil {
			c.log.Warn("failed to register wire", "from", w.From, "to", w.To, "error", err)
		} else {
			c.log.Info("registered wire", "from", w.From, "to", w.To, "eid", wireEID, "machine_id", machineID)
		}
	}

	return nil
}

// ensureScenariosRepo creates the scenarios directory if it doesn't exist.
// Note: Git versioning is NOT used here to avoid nested git repos.
// Scenarios are versioned by their meta.version field.
// True GitOps (external repo, webhooks) can be added in a later phase.
func (c *ScenarioController) ensureScenariosRepo() error {
	if c.repoReady {
		return nil
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(c.repoPath, 0750); err != nil {
		return fmt.Errorf("failed to create scenarios directory: %w", err)
	}

	c.log.Info("scenarios directory ready", "path", c.repoPath)
	c.repoReady = true
	return nil
}

// pushScenarioToRacks publishes the scenario to racks.
// If specificRack is provided, it only pushes to that one.
// Otherwise, it iterates over s.Racks.
func (c *ScenarioController) pushScenarioToRacks(ctx context.Context, s *registry.Scenario, content []byte, specificRack string) error {
	now := time.Now().Unix()
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version)
	}

	// Determine targets
	var targets []string
	if specificRack != "" {
		targets = []string{specificRack}
	} else {
		for _, r := range s.Racks {
			if r.Name != "" {
				targets = append(targets, r.Name)
			}
		}
	}

	for _, rackName := range targets {
		// Get rack machineID from registry with retry
		var machineID uuid.UUID
		if c.store != nil {
			mid, err := c.waitForRack(ctx, rackName)
			if err != nil {
				c.log.Warn("skipping scenario push: rack not in registry", "rack", rackName)
				continue
			}
			machineID = mid
		}

		// Build payload
		payload := &fluxmsg.ScenarioPayload{
			Version:   s.Meta.Version,
			Name:      scenarioName,
			RackName:  rackName,
			MachineID: machineID,
			Timestamp: now,
		}

		// Marshal scenarios with updated IDs for the payload
		// We re-marshal because s now contains IDs that were generated during registration
		updatedContent, err := yaml.Marshal(s)
		if err == nil {
			payload.Scenario = updatedContent
		} else {
			c.log.Warn("failed to re-marshal scenario with IDs, sending original content", "error", err)
			payload.Scenario = content
		}

		data, err := payload.ToData()
		if err != nil {
			c.log.Warn("failed to serialize scenario payload", "rack", rackName, "error", err)
			continue
		}

		msg := fluxmsg.New()
		if id, err := c.idGen.NextFluxID(); err == nil {
			msg.FluxID = id
		}
		msg.Data = data

		subject := fluxmsg.SubjectScenarioPrefix + rackName + fluxmsg.SubjectScenarioSuffix
		if err := c.bus.Publish(ctx, subject, msg); err != nil {
			c.log.Warn("failed to publish scenario to rack", "rack", rackName, "subject", subject, "error", err)
		} else {
			c.log.Info("pushed scenario to rack", "rack", rackName, "version", s.Meta.Version)
		}
	}

	return nil
}

// waitForRack polls the registry for a rack registration for up to 5 seconds.
func (c *ScenarioController) waitForRack(ctx context.Context, name string) (uuid.UUID, error) {
	deadline := time.Now().Add(c.waitTimeout)
	for time.Now().Before(deadline) {
		mid, err := c.store.GetRackByName(ctx, name)
		if err == nil {
			return mid, nil
		}
		select {
		case <-ctx.Done():
			return uuid.Nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			// retry
		}
	}
	return uuid.Nil, fmt.Errorf("timeout waiting for rack registration")
}
