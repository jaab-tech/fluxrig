// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/translation"
)

// ScenarioPublisher defines the interface for publishing scenarios to racks.
type ScenarioPublisher interface {
	Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error
}

// ErrUnknownDeployTarget is returned by Activate when a gear deploys to a name
// that is not an active Rack in the registry. The scenario stays imported: the
// caller can enroll the Rack and activate again.
var ErrUnknownDeployTarget = errors.New("unknown or inactive target")

// ErrUnresolvableGroupTarget is returned by Activate when a gear deploys to a
// name the scenario itself declares as a Rack group (a registry.RackTarget
// with Group set). The scenario's own syntax is valid: registry.Scenario.Validate
// accepts a group target and checks that the group carries match labels. But
// nothing in the registry today lets a live Rack carry labels, so there is no
// way to tell which active Racks, if any, belong to the group: matching one at
// Activate time is not implemented, and a gear pinned to a group target will
// always fail here until it is. This is deliberately a different sentinel from
// ErrUnknownDeployTarget: enrolling a Rack cannot fix this one.
var ErrUnresolvableGroupTarget = errors.New("deploy target names a Rack group, which cannot be matched against active Racks yet")

// ErrNoRackReached is returned by Activate when it has one or more targets but
// delivered to none of them. The scenario is still active: a Rack enrolling
// afterwards reaches it through PushActiveToRack. This is a distinct sentinel
// from ErrUnknownDeployTarget because a caller with an asynchronous enrollment
// path (a Mixer activating a startup scenario before any Rack has connected)
// needs to tell "nobody is there yet, and that is expected" apart from "this
// scenario names a target that will never exist."
var ErrNoRackReached = errors.New("reached no target rack")

// ScenarioManager defines the interface for managing scenarios.
type ScenarioManager interface {
	Import(ctx context.Context, content []byte, dryRun bool) (string, error)
	Activate(ctx context.Context, name string) error
	CurrentVersion() string
	CurrentName() string
	GetActiveScenario() *registry.Scenario
}

// TopologyStore keeps the rows derived from the active scenario (the
// scenario entity, its gears, ports and wires). They are local to each
// Mixer and rebuilt from the scenario, not shared.
type TopologyStore interface {
	ClearScenarioEntities(ctx context.Context) error
	RegisterScenario(ctx context.Context, eid uuid.UUID, name, version string, gearCount, wireCount int, mixerID uuid.UUID) error
	RegisterGear(ctx context.Context, eid uuid.UUID, name, gearType, mode, bind, connect string, scenarioID, machineID uuid.UUID, ports map[string]uuid.UUID, mixerID uuid.UUID) error
	RegisterPort(ctx context.Context, eid uuid.UUID, name string, portType uint16, gearID, scenarioID, machineID, mixerID uuid.UUID) error
	RegisterWire(ctx context.Context, eid uuid.UUID, fromName, toName string, fromID, toID, scenarioID, machineID, mixerID uuid.UUID) error
	GetEntityIDByName(ctx context.Context, name string) (uuid.UUID, error)
}

// RackDirectory answers which Racks exist and are active.
type RackDirectory interface {
	List(ctx context.Context, status string) ([]*registry.Rack, error)
	GetRackByName(ctx context.Context, name string) (uuid.UUID, error)
}

// ScenarioRepository keeps scenario files and the active-scenario pointer.
type ScenarioRepository interface {
	PutScenario(ctx context.Context, name string, content []byte) error
	GetScenario(ctx context.Context, name string) ([]byte, error)
	ListScenarios(ctx context.Context) ([]string, error)
	SetActive(ctx context.Context, active registry.ActiveScenario) error
	GetActive(ctx context.Context) (registry.ActiveScenario, error)
}

// ScenarioController manages the lifecycle of the Active Scenario.
type ScenarioController struct {
	log    *slog.Logger
	topo   TopologyStore
	racks  RackDirectory
	repo   ScenarioRepository
	idGen  *idgen.IDGenerator
	bus    ScenarioPublisher // For pushing scenarios to racks
	mu     sync.Mutex
	active *registry.Scenario // Currently active scenario
	// activeYAML is the active scenario exactly as it was registered and
	// pushed. Every push sends these bytes and never re-encodes the struct:
	// YAML does not round-trip every value (a "\n" delimiter comes back
	// empty), and a Rack keeps its gears only when a push matches its copy
	// byte for byte.
	activeYAML  []byte
	mixerID     uuid.UUID
	waitTimeout time.Duration
	specs       manager.Manager // resolves the spec artefacts a scenario names

	// deliveryMu guards the delivery record. It is separate from mu, which
	// Activate holds for the whole push: PushActiveToRackSince consults the
	// record from the enrollment path without waiting on an activation.
	deliveryMu sync.Mutex
	// activation counts activations. delivered records, per Rack, the
	// activation it last received and when, so the push that follows an
	// enrollment does not send a Rack the scenario that the activation has
	// just sent it: every push restarts the Rack's gears.
	activation uint64
	delivered  map[string]delivery
}

// delivery is one push of the active scenario to one Rack.
type delivery struct {
	activation uint64
	at         time.Time
}

// WithSpecStore gives the controller the store it resolves spec references
// against. Without one, a scenario naming a stored spec is still pushed: the
// rack will fail to resolve it and say so, which is a better failure than a
// scenario silently shipped without the protocol it speaks.
func (c *ScenarioController) WithSpecStore(m manager.Manager) *ScenarioController {
	c.specs = m
	return c
}

// NewScenarioController builds the controller. topo and racks may be nil in
// a test that exercises neither; repo is required.
func NewScenarioController(log *slog.Logger, topo TopologyStore, racks RackDirectory, repo ScenarioRepository, ig *idgen.IDGenerator, mixerID uuid.UUID, waitTimeout time.Duration) *ScenarioController {
	return &ScenarioController{
		log:         log.With("flux.type", "SCENARIO", "flux.name", "scenario-ctrl"),
		topo:        topo,
		racks:       racks,
		repo:        repo,
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

	// 3. Persist in the registry, by name.
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version)
	}
	safeName := sanitizeName(scenarioName)

	if err := c.repo.PutScenario(ctx, safeName, content); err != nil {
		return safeName, fmt.Errorf("failed to store scenario: %w", err)
	}

	c.log.Info("scenario imported", "name", safeName, "version", s.Meta.Version)
	return safeName, nil
}

// validateDeployTargets checks that all gear deploy targets exist as active racks in the registry.
// A target that names a Rack group the scenario declares (rather than an
// individual Rack) is refused with ErrUnresolvableGroupTarget: see its doc
// comment for why groups cannot be resolved here yet.
func (c *ScenarioController) validateDeployTargets(ctx context.Context, s *registry.Scenario) error {
	if c.racks == nil {
		c.log.Warn("validateDeployTargets: no rack directory, skipping validation")
		return nil
	}

	// Get all active racks from the registry
	activeRacks, err := c.racks.List(ctx, "active")
	if err != nil {
		return fmt.Errorf("failed to list active racks: %w", err)
	}
	c.log.Debug("validateDeployTargets: active racks", "count", len(activeRacks))

	// Build a set of active rack names
	activeRackNames := make(map[string]bool, len(activeRacks))
	for _, rack := range activeRacks {
		if rack.Name != "" {
			activeRackNames[rack.Name] = true
		}
	}

	// Groups the scenario itself declares. registry.Scenario.Validate already
	// confirmed each one carries match labels; named here only so an
	// unresolvable group gets its own, honest error instead of looking like a
	// typo or a Rack that just needs to enroll.
	declaredGroups := make(map[string]bool)
	for _, r := range s.Racks {
		if r.Group != "" {
			declaredGroups[r.Group] = true
		}
	}

	// Check each gear's deploy target
	for _, g := range s.Gears {
		target, ok := g.Deploy.(string)
		if !ok || activeRackNames[target] {
			continue
		}
		if declaredGroups[target] {
			return fmt.Errorf("gear %s deploys to group %q: %w", g.Name, target, ErrUnresolvableGroupTarget)
		}
		return fmt.Errorf("gear %s deploys to %w '%s'", g.Name, ErrUnknownDeployTarget, target)
	}

	return nil
}

// Activate loads a scenario by name and pushes it to racks.
// Use this to switch between stored scenarios.
// PushActiveToRack pushes the currently active scenario to a specific rack.
func (c *ScenarioController) PushActiveToRack(ctx context.Context, rackName string) error {
	return c.PushActiveToRackSince(ctx, rackName, time.Time{})
}

// PushActiveToRackSince pushes the active scenario to a Rack that enrolled at
// since, unless the Rack has already received this activation after since:
// an activation that ran between the enrollment and this push reached it
// already, and a second push would restart its gears for nothing. A zero
// since always pushes.
func (c *ScenarioController) PushActiveToRackSince(ctx context.Context, rackName string, since time.Time) error {
	c.mu.Lock()
	active, content := c.active, c.activeYAML
	c.mu.Unlock()

	if active == nil {
		return nil // No active scenario to push
	}

	if !since.IsZero() {
		c.deliveryMu.Lock()
		d, ok := c.delivered[rackName]
		current := c.activation
		c.deliveryMu.Unlock()
		if ok && d.activation == current && d.at.After(since) {
			c.log.Info("rack already has the active scenario from its activation; not pushing it again", "rack", rackName)
			return nil
		}
	}

	if c.bus == nil {
		return fmt.Errorf("scenario bus not initialized")
	}

	return c.pushScenarioToRacks(ctx, active, content, rackName)
}

func (c *ScenarioController) Activate(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	safeName := sanitizeName(name)

	// 1. Load the scenario from the registry
	content, errLoad := c.repo.GetScenario(ctx, safeName)
	if errors.Is(errLoad, registry.ErrNotFound) {
		return fmt.Errorf("scenario not found: %s", safeName)
	}
	if errLoad != nil {
		return fmt.Errorf("failed to load scenario %s: %w", safeName, errLoad)
	}

	var s registry.Scenario
	if err := yaml.Unmarshal(content, &s); err != nil {
		return fmt.Errorf("failed to parse scenario: %w", err)
	}

	// 2a. Validate deploy targets against the live registry. Racks must
	// exist and be active before a scenario is pushed to them. Import
	// stays permissive on purpose so a scenario can be filed before
	// its racks enroll.
	if err := c.validateDeployTargets(ctx, &s); err != nil {
		return fmt.Errorf("deploy target validation failed: %w", err)
	}

	// 2. Register entities in DuckDB (if store available), before anything is
	// committed. The registry is the source of truth the rest of the Mixer
	// reads to know what is actually deployed, so a scenario must not be
	// declared active - on disk or in memory - while a failure here leaves
	// that registry half-cleared or never updated: validate the swap by
	// making it first, then commit, rather than committing and hoping.
	if c.topo != nil && c.idGen != nil {
		// Clean up existing scenario entities (gears, ports, wires, scenarios) to avoid name conflicts
		if err := c.topo.ClearScenarioEntities(ctx); err != nil {
			return fmt.Errorf("failed to clear existing scenario entities: %w", err)
		}

		if err := c.registerScenarioEntities(ctx, &s, false); err != nil {
			return fmt.Errorf("failed to register scenario entities: %w", err)
		}
	}

	// 3. Move the active pointer. The topology above already reflects this
	// scenario. The pointer is what a restarted Mixer reads, so a failure
	// here is an error: the scenario must not be active in memory only.
	registered, errEnc := yaml.Marshal(&s)
	if errEnc != nil {
		return fmt.Errorf("failed to encode the registered scenario: %w", errEnc)
	}
	if err := c.repo.SetActive(ctx, registry.ActiveScenario{Name: safeName, Version: s.Meta.Version, Content: registered}); err != nil {
		return fmt.Errorf("failed to move the active scenario pointer: %w", err)
	}

	// 4. Set as active scenario in memory
	c.active = &s
	c.activeYAML = registered
	c.deliveryMu.Lock()
	c.activation++
	c.deliveryMu.Unlock()

	// 5. Push scenario to connected racks via NATS. The active pointer, the
	// in-memory scenario and the registry entities above are already
	// committed by this point, so a zero-delivery push does not roll them
	// back: the scenario genuinely is the Mixer's active one, and a Rack
	// that enrolls later still gets it through PushActiveToRack. But the
	// caller must not be told the activation reached anyone when it did
	// not, so the error is returned rather than only logged.
	if c.bus != nil {
		if err := c.pushScenarioToRacks(ctx, &s, registered, ""); err != nil {
			c.log.Warn("failed to push scenario to racks", "error", err)
			return fmt.Errorf("scenario %q is active but was not delivered: %w", safeName, err)
		}
	}

	c.log.Info("scenario activated", "name", safeName, "version", s.Meta.Version)
	return nil
}

// List returns the names of the stored scenarios.
func (c *ScenarioController) List(ctx context.Context) ([]string, error) {
	return c.repo.ListScenarios(ctx)
}

// GetActiveName returns the name the active pointer holds, or "" when no
// scenario was ever activated.
func (c *ScenarioController) GetActiveName(ctx context.Context) string {
	active, err := c.repo.GetActive(ctx)
	if err != nil {
		return ""
	}
	return active.Name
}

// LoadActive restores the active scenario a previous run left, at startup.
// It rebuilds the local topology rows and sets the scenario in memory, so the
// API reports it and a Rack that enrolls later receives it. It pushes nothing:
// Racks that already run the scenario keep running it undisturbed. A missing
// pointer is not an error: nothing was ever activated.
func (c *ScenarioController) LoadActive(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	active, err := c.repo.GetActive(ctx)
	if errors.Is(err, registry.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read the active scenario pointer: %w", err)
	}
	name := active.Name
	// The registered form carries the entity IDs the Racks received, so a
	// push after the restart is byte-identical to what they run, and a Rack
	// that resumed its own copy keeps its gears. The imported file is the
	// fallback for a pointer written without it.
	content := active.Content
	fromImport := len(content) == 0
	if fromImport {
		if content, err = c.repo.GetScenario(ctx, name); err != nil {
			return fmt.Errorf("active scenario %s: %w", name, err)
		}
	}
	var s registry.Scenario
	if err = yaml.Unmarshal(content, &s); err != nil {
		return fmt.Errorf("active scenario %s: %w", name, err)
	}
	if c.topo != nil && c.idGen != nil {
		if err = c.topo.ClearScenarioEntities(ctx); err != nil {
			return fmt.Errorf("failed to clear scenario entities: %w", err)
		}
		if err = c.registerScenarioEntities(ctx, &s, true); err != nil {
			return fmt.Errorf("failed to register scenario entities: %w", err)
		}
	}
	if fromImport {
		// Only the imported file was kept: this is the one case the
		// registered form has to be encoded here, after registration.
		if content, err = yaml.Marshal(&s); err != nil {
			return fmt.Errorf("active scenario %s: %w", name, err)
		}
	}
	c.active = &s
	c.activeYAML = content
	c.log.Info("active scenario restored", "name", name, "version", s.Meta.Version)
	return nil
}

// sanitizeName makes a caller-supplied name safe wherever this package uses
// one structurally: as a filesystem path component (scenario storage) or as a
// NATS subject token (the enrollment reply topic). Restricting to
// alphanumeric, hyphens and underscores happens to satisfy both: neither path
// traversal characters nor the subject separators/wildcards (".", "*", ">")
// survive it.
func sanitizeName(name string) string {
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
		safeName = "unnamed"
	}

	return safeName
}

// registerScenarioEntities persists scenario, gears, and wires to the registry.
// It also activates any racks defined in the scenario.
// registerScenarioEntities writes the topology rows for s. restoring is set
// when a starting Mixer restores its active scenario: then a Rack is looked
// up without waiting and a missing one is left unresolved. The rows are a
// local cache, and waiting would hold up startup before the Mixer can answer
// the very Hello that would bring that Rack back.
func (c *ScenarioController) registerScenarioEntities(ctx context.Context, s *registry.Scenario, restoring bool) error {
	// Register Scenario
	scenarioEID := c.idGen.NextEntityID(idgen.EntityScenario)
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version) // Fallback if no name
	}
	if err := c.topo.RegisterScenario(ctx, scenarioEID, scenarioName, s.Meta.Version, len(s.Gears), len(s.Wires), c.mixerID); err != nil {
		return fmt.Errorf("failed to register scenario: %w", err)
	}
	c.log.Info("registered scenario", "name", scenarioName, "eid", scenarioEID)

	// Build a map of rack name -> machineID. Never transition rack status
	// here: an operator approves a Rack (pending -> active) through its own
	// deliberate flow, not as a side effect of importing a scenario that
	// happens to name it. waitForRack (via GetRackByName) only matches an
	// already-active Rack, so one that is still pending times out here with
	// a clear error instead of being silently promoted.
	rackMachineIDs := make(map[string]uuid.UUID)
	for _, rack := range s.Racks {
		if rack.Name != "" {
			if restoring {
				if c.racks == nil {
					continue
				}
				if machineID, err := c.racks.GetRackByName(ctx, rack.Name); err == nil {
					rackMachineIDs[rack.Name] = machineID
				}
				continue
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

		// Idempotent Gear Registration: keep the ID the scenario already
		// carries (a restored active scenario), else reuse the one registered
		// under this name, else mint one.
		gearEID := g.ID
		if gearEID == uuid.Nil {
			var errID error
			if gearEID, errID = c.topo.GetEntityIDByName(ctx, g.Name); errID != nil {
				gearEID = c.idGen.NextEntityID(idgen.EntityGear)
			}
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
		inPortID := g.Ports["in"]
		if inPortID == uuid.Nil {
			var errID error
			if inPortID, errID = c.topo.GetEntityIDByName(ctx, inPortName); errID != nil {
				inPortID = c.idGen.NextEntityID(idgen.EntityPortInput)
			}
		}

		if errReg := c.topo.RegisterPort(ctx, inPortID, inPortName, uint16(idgen.EntityPortInput), gearEID, scenarioEID, machineID, c.mixerID); errReg != nil {
			c.log.Warn("failed to register input port", "name", inPortName, "error", errReg)
		}

		// Output Port
		outPortName := g.Name + ".out"
		outPortID := g.Ports["out"]
		if outPortID == uuid.Nil {
			var errID error
			if outPortID, errID = c.topo.GetEntityIDByName(ctx, outPortName); errID != nil {
				outPortID = c.idGen.NextEntityID(idgen.EntityPortOutput)
			}
		}

		if err := c.topo.RegisterPort(ctx, outPortID, outPortName, uint16(idgen.EntityPortOutput), gearEID, scenarioEID, machineID, c.mixerID); err != nil {
			c.log.Warn("failed to register output port", "name", outPortName, "error", err)
		}

		// Store Port IDs for Gear and Wire registration
		ports := map[string]uuid.UUID{
			"in":  inPortID,
			"out": outPortID,
		}
		g.Ports = ports // Assign to spec for Rack transmission
		gearPortIDs[g.Name] = ports

		if err := c.topo.RegisterGear(ctx, gearEID, g.Name, g.Type, mode, bind, connect, scenarioEID, machineID, ports, c.mixerID); err != nil {
			c.log.Warn("failed to register gear", "name", g.Name, "error", err)
		} else {
			c.log.Info("registered gear", "name", g.Name, "eid", gearEID, "machine_id", machineID)
		}
	}

	// Register Wires - use the machineID of the source gear's rack
	for i := range s.Wires {
		w := &s.Wires[i] // Pointer to modify
		wireEID := w.ID  // kept from a restored active scenario
		if wireEID == uuid.Nil {
			wireEID = c.idGen.NextEntityID(idgen.EntityWire)
		}
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

		if err := c.topo.RegisterWire(ctx, wireEID, w.From, w.To, fromID, toID, scenarioEID, machineID, c.mixerID); err != nil {
			c.log.Warn("failed to register wire", "from", w.From, "to", w.To, "error", err)
		} else {
			c.log.Info("registered wire", "from", w.From, "to", w.To, "eid", wireEID, "machine_id", machineID)
		}
	}

	return nil
}

// ensureScenariosRepo creates the scenarios directory if it doesn't exist.
// pushScenarioToRacks publishes the scenario to racks.
// If specificRack is provided, it only pushes to that one.
// Otherwise, it iterates over s.Racks.
func (c *ScenarioController) pushScenarioToRacks(ctx context.Context, s *registry.Scenario, content []byte, specificRack string) error {
	now := time.Now().Unix()
	scenarioName := s.Meta.Name
	if scenarioName == "" {
		scenarioName = fmt.Sprintf("scenario-%s", s.Meta.Version)
	}

	// Determine targets: the scenario's own racks: list, unioned with every
	// gear's deploy: pin. A scenario that pins gears to a Rack but omits it
	// from racks: (or omits racks: altogether) must still reach that Rack;
	// racks: alone used to be the only source, so such a scenario silently
	// pushed to nobody. A Global Gear (no deploy) needs neither: it reaches
	// every connected Rack through its own subscription, not this list.
	var targets []string
	if specificRack != "" {
		targets = []string{specificRack}
	} else {
		seen := make(map[string]bool)
		add := func(name string) {
			if name != "" && !seen[name] {
				seen[name] = true
				targets = append(targets, name)
			}
		}
		for _, r := range s.Racks {
			add(r.Name)
		}
		for _, g := range s.Gears {
			if name, ok := g.Deploy.(string); ok {
				add(name)
			}
		}
		// A Global Gear (no deploy) runs on every Rack. A Rack that enrolls
		// later receives the scenario through PushActiveToRackSince; the
		// Racks already enrolled receive it here.
		if hasGlobalGear(s) && c.racks != nil {
			active, err := c.racks.List(ctx, "active")
			if err != nil {
				c.log.Warn("cannot list the active racks for the scenario's global gears", "error", err)
			}
			for _, r := range active {
				add(r.Name)
			}
		}
	}

	// A scenario without push targets activates into nothing. Say so
	// loudly: this used to be a silent no-op (HTTP 200, zero delivery).
	if len(targets) == 0 {
		if hasGlobalGear(s) {
			c.log.Warn("scenario has only global gears and no rack is active yet: a rack that enrolls later receives it", "name", scenarioName)
		} else {
			c.log.Warn("scenario has no push targets: activation is a no-op", "name", scenarioName)
		}
	}

	delivered := 0
	for _, rackName := range targets {
		// Get rack machineID from registry with retry
		var machineID uuid.UUID
		if c.racks != nil {
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

		// content is the scenario as registered, IDs included, encoded once
		// at activation. It is sent as is: re-encoding s could change it.
		payload.Scenario = content

		payload.Specs = c.collectSpecs(ctx, s)

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
			delivered++
			c.recordDelivery(rackName)
		}
	}

	// A scenario with nobody to reach (no racks:, no gear deploys) is a legitimate
	// activation of an empty or global-only scenario: only fail when there was
	// somewhere to go and every attempt failed.
	if len(targets) > 0 && delivered == 0 {
		return fmt.Errorf("%w: %d target rack(s): %v", ErrNoRackReached, len(targets), targets)
	}

	return nil
}

// recordDelivery notes that rackName received the current activation now.
func (c *ScenarioController) recordDelivery(rackName string) {
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	if c.delivered == nil {
		c.delivered = make(map[string]delivery)
	}
	c.delivered[rackName] = delivery{activation: c.activation, at: time.Now()}
}

// hasGlobalGear reports whether a scenario has a gear with no deploy target,
// which runs on every Rack.
func hasGlobalGear(s *registry.Scenario) bool {
	for _, g := range s.Gears {
		switch d := g.Deploy.(type) {
		case nil:
			return true
		case string:
			if d == "" {
				return true
			}
		}
	}
	return false
}

// waitForRack polls the registry for a rack registration for up to 5 seconds.
func (c *ScenarioController) waitForRack(ctx context.Context, name string) (uuid.UUID, error) {
	deadline := time.Now().Add(c.waitTimeout)
	for time.Now().Before(deadline) {
		mid, err := c.racks.GetRackByName(ctx, name)
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

// specRefFromConfig reads the spec a gear config names, under either the current
// key or the legacy alias the codec still accepts.
func specRefFromConfig(cfg map[string]any) string {
	for _, key := range []string{"spec_path", "spec"} {
		if v, ok := cfg[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// storeRef splits a `name:tag` reference. A path is not one, and neither is a
// bare hash — a hash needs no name to be resolved, so it is already portable and
// nothing has to travel for it.
func storeRef(ref string) (name, tag string, ok bool) {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		return "", "", false
	}
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// collectSpecs gathers the spec artefacts a scenario names so they travel with
// it. A rack resolves a spec against its own store, and until now nothing put
// anything there: a scenario could name a protocol the rack had never seen, or —
// worse — a different file that happened to sit at the same path.
func (c *ScenarioController) collectSpecs(ctx context.Context, s *registry.Scenario) []fluxmsg.SpecArtifact {
	if c.specs == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []fluxmsg.SpecArtifact
	type pending struct{ ref, gear string }
	var work []pending
	for _, g := range s.Gears {
		if ref := specRefFromConfig(g.Config); ref != "" {
			work = append(work, pending{ref, g.Name})
		}
	}
	// A translation spec names more artefacts (its protocol specs and its
	// lookup sources), and the rack resolves those from its store too, so
	// they travel as well.
	for len(work) > 0 {
		p := work[0]
		work = work[1:]
		name, tag, ok := storeRef(p.ref)
		if !ok || seen[p.ref] {
			continue
		}
		seen[p.ref] = true
		content, err := c.specs.Load(ctx, p.ref)
		if err != nil {
			// Pushing anyway is deliberate: the rack reports an unresolvable spec
			// against the reference the scenario actually names, which is more use
			// than the Mixer refusing to deploy over a spec it cannot find.
			c.log.Warn("spec named by scenario is not in the store", "ref", p.ref, "gear", p.gear, "error", err)
			continue
		}
		out = append(out, fluxmsg.SpecArtifact{Name: name, Tag: tag, Content: content})
		if refs, isTranslation := translation.References(content); isTranslation {
			for _, r := range refs {
				work = append(work, pending{r, p.gear})
			}
		}
	}
	return out
}
