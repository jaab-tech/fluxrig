package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Manager orchestrates the lifecycle of Gears on a Rack.
type Manager struct {
	bus       bus.Bus
	idGen     *idgen.IDGenerator // Wrapper that satisfies sdk.IDGenerator
	factory   *gears.Factory
	machineID uint64
	rackName  string

	activeGears map[string]sdk.NativeGear
	gearPorts   map[string]map[string]uint64 // gear -> port -> entityID
	gearIDs     map[string]uint64            // gear -> entityID
	activeSubs  []bus.Subscription
	mu          sync.Mutex
}

func NewManager(log *slog.Logger, b bus.Bus, ig *idgen.IDGenerator, mid uint64, name string) *Manager {
	return &Manager{
		bus:         b,
		idGen:       ig,
		factory:     gears.NewFactory(),
		machineID:   mid,
		rackName:    name,
		activeGears: make(map[string]sdk.NativeGear),
		gearPorts:   make(map[string]map[string]uint64),
		gearIDs:     make(map[string]uint64),
	}
}

// logger returns the current OTel-connected logger for the runtime component.
// Uses slog.Default() dynamically to ensure logs go to the current provider
// even after UpdateIdentity reinitializes telemetry.
// Does NOT set entity type - inherits RACK from slog.Default().
// Source file runtime.go:XX identifies code location.
func (m *Manager) logger() *slog.Logger {
	return slog.Default().With("component", "RACK")
}

// GearContextImpl implements sdk.GearContext
type GearContextImpl struct {
	ctx       context.Context
	cfg       map[string]any
	name      string
	machineID uint64
	logger    *slog.Logger
	idGen     sdk.IDGenerator
}

func (g *GearContextImpl) Context() context.Context { return g.ctx }
func (g *GearContextImpl) Config() map[string]any   { return g.cfg }
func (g *GearContextImpl) GearName() string         { return g.name }
func (g *GearContextImpl) MachineID() uint64        { return g.machineID }
func (g *GearContextImpl) Logger() *slog.Logger     { return g.logger }
func (g *GearContextImpl) IDGen() sdk.IDGenerator   { return g.idGen }

// ApplyScenario diffs and applies the scenario.
func (m *Manager) ApplyScenario(ctx context.Context, sc *registry.Scenario) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger().Info("applying scenario", "version", sc.Meta.Version)

	// 1. Stop Existing
	m.stopAll()

	// 2. Identify Gears for this Rack
	for _, gSpec := range sc.Gears {
		target, ok := gSpec.Deploy.(string)
		if !ok || target != m.rackName {
			continue
		}

		// 3. Create Gear
		gear, err := m.factory.Create(gSpec.Type)
		if err != nil {
			return fmt.Errorf("create gear %s error: %w", gSpec.Name, err)
		}

		// 3b. Generate Port IDs (Implicit In/Out for Phase 3)
		var inID, outID uint64

		if gSpec.Ports != nil {
			// Use IDs assigned by Mixer
			if id, ok := gSpec.Ports["in"]; ok {
				inID = id
			}
			if id, ok := gSpec.Ports["out"]; ok {
				outID = id
			}
		}

		// Fallback for missing IDs (should not happen if from Mixer)
		if inID == 0 {
			inID = m.idGen.NextEntityID(idgen.EntityPortInput)
		}
		if outID == 0 {
			outID = m.idGen.NextEntityID(idgen.EntityPortOutput)
		}

		m.gearPorts[gSpec.Name] = map[string]uint64{
			"in":  inID,
			"out": outID,
		}

		// 3c. Generate or Use Gear ID (Prefer Spec ID if from Mixer)
		var gearID uint64
		if gSpec.ID > 0 {
			gearID = gSpec.ID
		} else {
			gearID = m.idGen.NextEntityID(idgen.EntityGear)
		}
		m.gearIDs[gSpec.Name] = gearID

		// 4. Init Gear
		gCtx := &GearContextImpl{
			ctx:       ctx,
			cfg:       gSpec.Config,
			name:      gSpec.Name,
			machineID: m.machineID,
			// Override component to GEAR and name to gear name
			logger: logger.WithComponent(m.logger(), logger.TypeGear, gSpec.Name),
			idGen:  m.idGen,
		}

		if err := gear.Init(gCtx); err != nil {
			return fmt.Errorf("init gear %s error: %w", gSpec.Name, err)
		}

		m.activeGears[gSpec.Name] = gear
	}

	// 5. Wire Up (Subscriptions)
	for _, wire := range sc.Wires {
		toGear, toPort := parsePortRef(wire.To)

		targetGear, check := m.activeGears[toGear]
		if !check {
			continue
		}

		// Get Port ID for "in" (assuming toPort maps to "in" for simple gears, or verify)
		// For simple_tcp, "in" is implicit listener or Process target.
		// If wire.To is "gateway.in", we use "in" ID.
		portID := m.gearPorts[toGear]["in"]
		// If explicit port logic exists, lookup by toPort.
		if id, ok := m.gearPorts[toGear][toPort]; ok {
			portID = id
		}

		subject := fmt.Sprintf("flux.gear.%s", wire.From)

		// Determine Wire Label (ID vs Name)
		wireLabel := fmt.Sprintf("%s -> %s", wire.From, wire.To)
		if wire.ID > 0 {
			wireLabel = fmt.Sprintf("%x", wire.ID)
		}

		sub, err := m.bus.Subscribe(subject, func(msg *fluxmsg.FluxMsg) {
			// Append Hop (Arrival at Target Port)
			msg.Path = append(msg.Path, &fluxmsg.Hop{
				GearID: m.gearIDs[toGear],
				PortID: portID,
				TsNano: time.Now().UnixNano(),
			})

			// TRACE Logging: Bus Receive (Port In)
			if m.logger().Enabled(context.Background(), logger.LevelTrace) {
				m.logger().Log(context.Background(), logger.LevelTrace, "Bus Receive",
					"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
					"wire", wireLabel,
					"port_id", fmt.Sprintf("0x%x", portID),
					"gear", toGear,
				)
			} else if os.Getenv("FLUXRIG_DEBUG") == "true" {
				m.logger().Debug("FluxMsg received",
					"flux_id", fmt.Sprintf("%x", msg.FluxID),
					"wire", wireLabel,
				)
			}

			// Delivery to Target Gear
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := targetGear.Process(ctx, msg)
			if err != nil {
				m.logger().Error("processing failed", "gear", toGear, "error", err)
				return
			}

			if os.Getenv("FLUXRIG_DEBUG") == "true" && resp != nil {
				m.logger().Debug("FluxMsg processed", "flux_id", fmt.Sprintf("%x", msg.FluxID))
			}
		})
		if err != nil {
			return fmt.Errorf("subscribe wire %s->%s error: %w", wire.From, wire.To, err)
		}
		m.activeSubs = append(m.activeSubs, sub)
		m.logger().Info("wired", "wire", wireLabel)
	}

	// 6. Start Gears
	for name, g := range m.activeGears {
		outSubject := fmt.Sprintf("flux.gear.%s.out", name)

		// Port ID for implicit out
		portID := m.gearPorts[name]["out"]

		// Gear EntityID? We don't have it explicitly stored/assigned here yet,
		// but we can generate one or assume one.
		// For Hop, we need GearID.
		// Let's generate a GearID too? Or retrieve?
		// User requirement: "fluxEntityIDs assigned".
		// I'll add Gear ID generation too.

		emitFunc := func(msg *fluxmsg.FluxMsg) {
			// Ensure FluxID exists
			if msg.FluxID == 0 {
				id, _ := m.idGen.NextFluxID()
				msg.FluxID = id
			}

			// Append Hop
			msg.Path = append(msg.Path, &fluxmsg.Hop{
				GearID: m.gearIDs[name],
				PortID: portID,
				TsNano: time.Now().UnixNano(),
			})

			// TRACE Logging: Bus Emit (Port Out)
			if m.logger().Enabled(context.Background(), logger.LevelTrace) {
				m.logger().Log(context.Background(), logger.LevelTrace, "Bus Emit",
					"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
					"port_id", fmt.Sprintf("0x%x", portID),
					"gear", name,
					"subject", outSubject,
				)
			}

			if err := m.bus.Publish(outSubject, msg); err != nil {
				m.logger().Error("emit failed", "gear", name, "error", err)
			}
		}

		if err := g.Start(ctx, emitFunc); err != nil {
			return fmt.Errorf("start gear %s error: %w", name, err)
		}
		m.logger().Info("gear started", "name", name)
	}

	return nil
}

// Shutdown stops all active gears and subscriptions.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopAll()
}

func (m *Manager) stopAll() {
	for _, sub := range m.activeSubs {
		_ = sub.Unsubscribe()
	}
	m.activeSubs = nil

	for name, g := range m.activeGears {
		_ = g.Stop()
		m.logger().Debug("gear stopped", "name", name)
	}
	m.activeGears = make(map[string]sdk.NativeGear)
	m.gearPorts = make(map[string]map[string]uint64)
	m.gearIDs = make(map[string]uint64)
}

func parsePortRef(ref string) (string, string) {
	if idx := strings.LastIndex(ref, "."); idx != -1 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}
