// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// Manager orchestrates the lifecycle of Gears on a Rack.
type Manager struct {
	bus                bus.Bus
	idGen              *idgen.IDGenerator // Wrapper that satisfies sdk.IDGenerator
	mgr                manager.Manager    // Spec/Scenario Manager
	factory            *gears.Factory
	machineID          uint64
	rackName           string
	timeout            time.Duration
	convergenceTimeout time.Duration
	handshakeInterval  time.Duration

	activeGears map[string]sdk.NativeGear
	gearPorts   map[string]map[string]uint64 // gear -> port -> entityID
	gearIDs     map[string]uint64            // gear -> entityID
	activeSubs  []bus.Subscription
	hotSubjects map[string]chan struct{}
	mu          sync.Mutex
}

func NewManager(machineID uint64, name string, b bus.Bus, ig *idgen.IDGenerator, specMgr manager.Manager, opTimeout, convTimeout, handshakeInterval time.Duration) *Manager {
	return &Manager{
		bus:                b,
		idGen:              ig,
		mgr:                specMgr,
		factory:            gears.NewFactory(),
		machineID:          machineID,
		rackName:           name,
		timeout:            opTimeout,
		convergenceTimeout: convTimeout,
		handshakeInterval:  handshakeInterval,
		activeGears:        make(map[string]sdk.NativeGear),
		gearPorts:          make(map[string]map[string]uint64),
		gearIDs:            make(map[string]uint64),
		hotSubjects:        make(map[string]chan struct{}),
	}
}

// Start initiates the active lifecycle of the data-plane.
func (m *Manager) Start() error {
	// For now, Start is a placeholder as the heavy lifting is handled by ApplyScenario
	// and waitForConvergence during the Relentless Handshake.
	return nil
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
	bus       bus.Bus
	mgr       manager.Manager
	ctrl      ctrl.ControlPlane
}

func (g *GearContextImpl) Context() context.Context { return g.ctx }
func (g *GearContextImpl) Config() map[string]any   { return g.cfg }
func (g *GearContextImpl) GearName() string         { return g.name }
func (g *GearContextImpl) MachineID() uint64        { return g.machineID }
func (g *GearContextImpl) Logger() *slog.Logger     { return g.logger }
func (g *GearContextImpl) IDGen() sdk.IDGenerator   { return g.idGen }
func (g *GearContextImpl) Bus() bus.Bus             { return g.bus }
func (g *GearContextImpl) Manager() manager.Manager { return g.mgr }
func (g *GearContextImpl) ControlPlane() any        { return g.ctrl }

// ApplyScenario diffs and applies the scenario.
func (m *Manager) ApplyScenario(ctx context.Context, sc *registry.Scenario) (err error) {
	m.mu.Lock()
	locked := true
	defer func() {
		if locked {
			m.mu.Unlock()
		}
	}()

	// Transactional cleanup: if we fail mid-way, ensure we don't leave partial state
	defer func() {
		if err != nil {
			// We need to ensure we have the lock for stopAll if it was released during convergence
			if !locked {
				m.mu.Lock()
				locked = true
			}
			m.logger().Warn("ApplyScenario failed, performing transactional cleanup", "error", err)
			m.stopAll()
		}
	}()

	m.logger().Info("applying scenario", "name", sc.Meta.Name, "version", sc.Meta.Version)

	// 1. Stop Existing
	m.stopAll()

	// 1b. Build Gear Deployment Map (name -> rack) for global wire resolution
	gearDeploy := make(map[string]string)
	for _, g := range sc.Gears {
		if target, ok := g.Deploy.(string); ok {
			gearDeploy[g.Name] = target
		}
	}

	// 2. Identify Gears for this Rack
	for _, gSpec := range sc.Gears {
		target, _ := gSpec.Deploy.(string)
		if target != "" && target != m.rackName {
			continue
		}

		// 3. Create Gear
		var gear sdk.NativeGear
		gear, err = m.factory.Create(gSpec.Type)
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
			bus:    m.bus,
			mgr:    m.mgr,
		}

		// MANDATORY: Check for NATS core before initializing Control Plane
		if core := m.bus.Core(); core != nil {
			if conn, ok := core.(*nats.Conn); ok {
				gCtx.ctrl = ctrl.NewNATSControlPlane(conn)
			}
		}

		if err = gear.Init(gCtx); err != nil {
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
		// For io_tcp, "in" is implicit listener or Process target.
		// If wire.To is "gateway.in", we use "in" ID.
		portID := m.gearPorts[toGear]["in"]
		// If explicit port logic exists, lookup by toPort.
		if id, ok := m.gearPorts[toGear][toPort]; ok {
			portID = id
		}

		// Resolve Source Rack for the 'From' endpoint
		fromGear, _ := parsePortRef(wire.From)
		sourceRack := gearDeploy[fromGear]
		if sourceRack == "" {
			// Fallback: If not in scenario gears, it might be a global/shared subject.
			// Default to flux.msg.global (or keep as is if we want to support legacy).
			// For standard FluxRig wires, sourceRack should always exist.
			m.logger().Warn("wire source gear not found in scenario", "gear", fromGear, "wire", wire.From)
			sourceRack = "global"
		}

		subject := fmt.Sprintf("flux.msg.%s.%s", sourceRack, wire.From)

		// Determine Wire Label (ID vs Name)
		wireLabel := fmt.Sprintf("%s -> %s", wire.From, wire.To)
		if wire.ID > 0 {
			wireLabel = fmt.Sprintf("%x", wire.ID)
		}

		var sub bus.Subscription
		sub, err = m.bus.Subscribe(subject, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
			// PROBE HANDLING
			if msg != nil && msg.Flags&fluxmsg.FlagSyncProbe != 0 {
				m.mu.Lock()
				if ch, ok := m.hotSubjects[subject]; ok {
					close(ch)
					delete(m.hotSubjects, subject)
					m.logger().Debug("subject converged (path is hot)", "subject", subject)
				}
				m.mu.Unlock()
				return
			}

			// Append Hop (Arrival at Target Port)
			msg.Path = append(msg.Path, &fluxmsg.Hop{
				GearID: m.gearIDs[toGear],
				PortID: portID,
				TsNano: time.Now().UnixNano(),
			})

			// TRACE Logging: Bus Receive (Port In)
			if m.logger().Enabled(ctx, logger.LevelTrace) {
				m.logger().Log(ctx, logger.LevelTrace, "Bus Receive",
					"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
					"wire", wireLabel,
					"gear", toGear,
					"port_id", fmt.Sprintf("0x%x", portID),
					"payload_hex", fmt.Sprintf("0x%x", msg.RawPayload),
					"meta", fmt.Sprintf("%v", msg.Metadata),
					"path", formatHops(msg.Path),
				)
			} else if os.Getenv("FLUXRIG_DEBUG") == "true" {
				m.logger().Debug("FluxMsg received",
					"flux_id", fmt.Sprintf("%x", msg.FluxID),
					"wire", wireLabel,
				)
			}

			// Delivery to Target Gear
			// Pass the INCOMING CONTEXT (carrying traces) to Process.
			timeoutCtx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()

			start := time.Now()
			resp, pErr := targetGear.Process(timeoutCtx, msg)
			duration := float64(time.Since(start).Microseconds()) / 1000.0 // Record in ms for consistency with metric name

			// INSTRUMENTATION: Gear Input
			if tm := telemetry.GetMetrics(); tm != nil {
				metricCtx := ctx
				attrs := metric.WithAttributes(attribute.String("gear", toGear))

				tm.GearMessagesIn.Add(metricCtx, 1, attrs)
				tm.GearProcessingTime.Record(metricCtx, duration, attrs)

				if pErr != nil {
					tm.GearErrors.Add(metricCtx, 1, attrs)
				}
			}

			if pErr != nil {
				m.logger().Error("processing failed", "gear", toGear, "error", pErr)
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
		m.hotSubjects[subject] = make(chan struct{})
		m.logger().Info("wired", "wire", wireLabel, "subject", subject)
	}

	// 4. Wait for connectivity convergence
	// Release lock while waiting for convergence to allow handlers to update state
	m.mu.Unlock()
	locked = false
	if errConv := m.waitForConvergence(ctx); errConv != nil {
		return errConv
	}
	m.mu.Lock()
	locked = true

	// 6. Start Gears
	for name, g := range m.activeGears {
		outSubject := fmt.Sprintf("flux.msg.%s.%s.out", m.rackName, name)

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
					"gear", name,
					"port_id", fmt.Sprintf("0x%x", portID),
					"subject", outSubject,
					"payload_hex", fmt.Sprintf("0x%x", msg.RawPayload),
					"meta", fmt.Sprintf("%v", msg.Metadata),
					"path", formatHops(msg.Path),
				)
			}
			m.logger().Debug("Bus Emit", "flux_id", fmt.Sprintf("0x%x", msg.FluxID), "subject", outSubject)

			// INSTRUMENTATION: Gear Output
			if tm := telemetry.GetMetrics(); tm != nil {
				metricCtx := context.Background()
				attrs := metric.WithAttributes(attribute.String("gear", name))
				tm.GearMessagesOut.Add(metricCtx, 1, attrs)
			}

			// Publish needs context. Where do we get it?
			// EmitFunc is usually called from internal goroutines.
			// We should probably encourage using context in EmitFunc or assume Background?
			// Actually the Start(..., emit) signature does not provide ctx to emit.
			// Ideally, emit should take context. But keeping SDK stable:
			// We validly assume that a Source emit starts a NEW Trace (Root Span) or continues if the goroutine has one.
			// Since we don't pass ctx to emit, we use Background().
			// LIMITATION: Source Gears that are "Processors" calling emit() asynchronously lose context unless they capture it.
			// BUT: NativeGear.Process returns *FluxMsg, so that path is synchronous/handled by runtime above.
			// So Emit() is mostly for "Source" gears (Active I/O).
			// Active Sources create ROOT spans. So Background is correct starting point.
			// We can wrap it in a "Source Span" here.
			// Start a Root Span for this emission (Source Gear)
			// Since SDK emit signature doesn't support context, we start a new trace here.
			// This handles Source Gears correctly. For processing gears using emit asynchronously,
			// this will restart the trace (limitation of current SDK).
			tracer := otel.GetTracerProvider().Tracer("fluxrig/runtime")
			spanName := fmt.Sprintf("gear_output %s", name)

			emitCtx, span := tracer.Start(context.Background(), spanName,
				trace.WithSpanKind(trace.SpanKindProducer),
				trace.WithAttributes(
					attribute.String("gear.name", name),
					attribute.String("messaging.system", "nats"),
					attribute.String("messaging.destination", outSubject),
				),
			)
			defer span.End()

			if pubErr := m.bus.Publish(emitCtx, outSubject, msg); pubErr != nil {
				if tm := telemetry.GetMetrics(); tm != nil {
					metricCtx := context.Background()
					attrs := metric.WithAttributes(attribute.String("gear", name))
					tm.GearErrors.Add(metricCtx, 1, attrs) // Count emit errors as gear errors? or just log?
					// Usually emit error is platform error, not gear logic error.
					// But impact is gear failed to processing.
				}
				m.logger().Error("emit failed", "gear", name, "error", pubErr)
			}
		}

		if err = g.Start(ctx, emitFunc); err != nil {
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

// Drain signals all active gears to stop accepting new work and complete pending work.
func (m *Manager) Drain(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger().Info("Draining Runtime Manager (Graceful Shutdown)")

	var wg sync.WaitGroup
	var errs []error
	var errMu sync.Mutex

	for name, g := range m.activeGears {
		name := name
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			// m.logger() is safe to call
			if err := g.Drain(ctx); err != nil {
				m.logger().Error("Drain Gear Failed", "name", name, "error", err)
				errMu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				errMu.Unlock()
			} else {
				m.logger().Info("Gear Drained", "name", name)
			}
		}()
	}

	// Wait for all gears or context timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger().Info("Runtime Manager Drained Successfully")
	case <-ctx.Done():
		m.logger().Warn("Runtime Manager Drain Timeout/Context Canceled", "error", ctx.Err())
		return ctx.Err()
	}

	if len(errs) > 0 {
		return fmt.Errorf("drain errors: %v", errs)
	}
	return nil
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
	m.hotSubjects = make(map[string]chan struct{})
}

func parsePortRef(ref string) (string, string) {
	if idx := strings.LastIndex(ref, "."); idx != -1 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}

func (m *Manager) waitForConvergence(ctx context.Context) error {
	if len(m.hotSubjects) == 0 {
		return nil
	}

	m.logger().Info("waiting for data-plane convergence", "subjects", len(m.hotSubjects))

	// 1. Setup Status Tracking
	pending := make(map[string]chan struct{})
	for s, ch := range m.hotSubjects {
		pending[s] = ch
	}

	// 2. Relentless Probe Loop
	// We re-emit probes every 500ms to handle NATS JetStream propagation lag.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.NewTimer(m.convergenceTimeout)
	defer timeout.Stop()

	emitProbes := func() {
		for subject := range pending {
			probe := fluxmsg.New()
			probe.Flags |= fluxmsg.FlagSyncProbe
			id, _ := m.idGen.NextFluxID()
			probe.FluxID = id

			m.logger().Debug("emitting sync probe", "subject", subject)
			if err := m.bus.Publish(ctx, subject, probe); err != nil {
				m.logger().Warn("failed to emit probe", "subject", subject, "error", err)
			}
		}
	}

	// Initial emission
	emitProbes()

	// 3. Parallel Collector
	for len(pending) > 0 {
		// We build the select cases dynamically or use a simple loop with default
		// Since we have a ticker, we can check channels in each iteration

		select {
		case <-ticker.C:
			// Re-emit for all still pending
			emitProbes()
		case <-timeout.C:
			var remaining []string
			for s := range pending {
				remaining = append(remaining, s)
			}
			return fmt.Errorf("timeout waiting for subject convergence: %v", remaining)
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Check if any pending subject has converged
			for s, ch := range pending {
				select {
				case <-ch:
					delete(pending, s)
					m.logger().Debug("subject converged (path is hot)", "subject", s)
				default:
					// still pending
				}
			}
			if len(pending) == 0 {
				break
			}
			// Small sleep to avoid CPU spinning in default case
			time.Sleep(10 * time.Millisecond)
		}
	}

	m.logger().Info("all data-plane subjects hot. starting gears")
	return nil
}

func formatHops(path []*fluxmsg.Hop) string {
	if len(path) == 0 {
		return "[]"
	}
	res := "["
	for i, h := range path {
		if i > 0 {
			res += ", "
		}
		res += fmt.Sprintf("{g:%x, p:%x}", h.GearID, h.PortID)
	}
	res += "]"
	return res
}
