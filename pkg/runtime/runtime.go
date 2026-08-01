// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	machineID          uuid.UUID
	rackName           string
	timeout            time.Duration
	convergenceTimeout time.Duration
	handshakeInterval  time.Duration

	activeGears   map[string]sdk.NativeGear
	gearPorts     map[string]map[string]uuid.UUID // gear -> port -> entityID
	gearIDs       map[string]uuid.UUID            // gear -> entityID
	activeSubs    []bus.Subscription
	hotSubjects   map[string]chan struct{}
	trace         bool
	debug         bool
	clusterPubKey []byte
	mu            sync.Mutex
	portsMu       sync.RWMutex // guards lazy port-ID assignment in gearPorts (emit hot path)
}

func NewManager(machineID uuid.UUID, name string, b bus.Bus, ig *idgen.IDGenerator, specMgr manager.Manager, opTimeout, convTimeout, handshakeInterval time.Duration, trace, debug bool, clusterPubKey []byte) *Manager {
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
		trace:              trace,
		debug:              debug,
		clusterPubKey:      clusterPubKey,
		activeGears:        make(map[string]sdk.NativeGear),
		gearPorts:          make(map[string]map[string]uuid.UUID),
		gearIDs:            make(map[string]uuid.UUID),
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

// portEmitter is the per-gear sdk.PortEmitter handed to a gear via GearContext.
// It binds a gear name to the manager's single emit path.
type portEmitter struct {
	m    *Manager
	gear string
}

// Emit sends msg out of the named port and surfaces a publish failure to the
// caller (per the sdk.PortEmitter contract), while never surfacing a panic:
// the emit path is recover-guarded in publishPort.
func (e *portEmitter) Emit(port string, msg *fluxmsg.FluxMsg) error {
	if port == "" {
		return fmt.Errorf("emit: empty port for gear %s", e.gear)
	}
	return e.m.publishPort(context.Background(), e.gear, port, msg)
}

// gearID returns a gear's entity ID under portsMu. The emit path and the
// subscribe handler call this concurrently with ApplyScenario/stopAll, which
// reassign gearIDs; portsMu (not m.mu) is the single guard for gearIDs and
// gearPorts, so all readers and writers must go through it.
func (m *Manager) gearID(gearName string) uuid.UUID {
	m.portsMu.RLock()
	defer m.portsMu.RUnlock()
	return m.gearIDs[gearName]
}

// portID returns the entity ID for a gear's port, lazily assigning one for
// named ports the Mixer did not pre-register (only in/out come pre-assigned).
// Guarded by portsMu because gears emit concurrently.
func (m *Manager) portID(gearName, port string) uuid.UUID {
	m.portsMu.RLock()
	if ports, ok := m.gearPorts[gearName]; ok {
		if id, ok := ports[port]; ok {
			m.portsMu.RUnlock()
			return id
		}
	}
	m.portsMu.RUnlock()

	m.portsMu.Lock()
	defer m.portsMu.Unlock()
	ports, ok := m.gearPorts[gearName]
	if !ok {
		ports = make(map[string]uuid.UUID)
		m.gearPorts[gearName] = ports
	}
	if id, ok := ports[port]; ok { // re-check after upgrading the lock
		return id
	}
	etype := idgen.EntityPortOutput
	if port == "in" || strings.HasPrefix(port, "in_") {
		etype = idgen.EntityPortInput
	}
	id := m.idGen.NextEntityID(etype)
	ports[port] = id
	return id
}

// publishPort is the single emit path for a gear's named output port. It is
// recover-guarded, nil-safe, hop-stamped and instrumented, and builds the
// subject flux.msg.<rack>.<gear>.<port>. `port` is a full port name such as
// "out", "out_scheme_a" or "error".
func (m *Manager) publishPort(ctx context.Context, gearName, port string, msg *fluxmsg.FluxMsg) (err error) {
	defer func() {
		if r := recover(); r != nil {
			m.logger().Error("gear emit panic recovered", "gear", gearName, "port", port, "panic", r)
			if tm := telemetry.GetMetrics(); tm != nil {
				tm.GearErrors.Add(context.Background(), 1,
					metric.WithAttributes(attribute.String("gear", gearName)))
			}
			err = fmt.Errorf("emit panic on %s.%s: %v", gearName, port, r)
		}
	}()

	if msg == nil {
		m.logger().Error("gear emitted a nil message; dropping", "gear", gearName, "port", port)
		return fmt.Errorf("emit: nil message on %s.%s", gearName, port)
	}
	if msg.FluxID == uuid.Nil {
		if id, e := m.idGen.NextFluxID(); e == nil {
			msg.FluxID = id
		}
	}

	subject := fmt.Sprintf("flux.msg.%s.%s.%s", m.rackName, gearName, port)
	msg.Path = append(msg.Path, &fluxmsg.Hop{
		GearID: m.gearID(gearName),
		PortID: m.portID(gearName, port),
		TSNano: time.Now().UnixNano(),
	})

	if tm := telemetry.GetMetrics(); tm != nil {
		tm.GearMessagesOut.Add(ctx, 1, metric.WithAttributes(
			attribute.String("gear", gearName),
			attribute.String("flux.name", gearName),
		))
	}

	tracer := otel.GetTracerProvider().Tracer("fluxrig/runtime")
	emitCtx, span := tracer.Start(ctx, fmt.Sprintf("gear_output %s", gearName),
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("gear.name", gearName),
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination", subject),
		),
	)
	defer span.End()

	if perr := m.bus.Publish(emitCtx, subject, msg); perr != nil {
		m.logger().Error("emit failed", "gear", gearName, "port", port, "error", perr)
		if tm := telemetry.GetMetrics(); tm != nil {
			tm.GearErrors.Add(context.Background(), 1,
				metric.WithAttributes(attribute.String("gear", gearName)))
		}
		return fmt.Errorf("emit %s.%s: %w", gearName, port, perr)
	}
	return nil
}

// GearContextImpl implements sdk.GearContext
type GearContextImpl struct {
	ctx           context.Context
	cfg           map[string]any
	name          string
	machineID     uuid.UUID
	logger        *slog.Logger
	idGen         sdk.IDGenerator
	bus           bus.Bus
	mgr           manager.Manager
	ctrl          ctrl.ControlPlane
	clusterPubKey []byte
	emitter       sdk.PortEmitter
	bindings      map[string]sdk.PortBinding
}

func (g *GearContextImpl) Context() context.Context { return g.ctx }
func (g *GearContextImpl) Emitter() sdk.PortEmitter { return g.emitter }

// Bindings implements sdk.BindingsProvider: the resolved terminus of each of
// this gear's output ports, from the activation-time wire-graph walk.
func (g *GearContextImpl) Bindings() map[string]sdk.PortBinding { return g.bindings }
func (g *GearContextImpl) Config() map[string]any               { return g.cfg }
func (g *GearContextImpl) GearName() string                     { return g.name }
func (g *GearContextImpl) MachineID() uuid.UUID                 { return g.machineID }
func (g *GearContextImpl) Logger() *slog.Logger                 { return g.logger }
func (g *GearContextImpl) IDGen() sdk.IDGenerator               { return g.idGen }
func (g *GearContextImpl) Bus() bus.Bus                         { return g.bus }
func (g *GearContextImpl) Manager() manager.Manager             { return g.mgr }
func (g *GearContextImpl) ControlPlane() any                    { return g.ctrl }
func (g *GearContextImpl) ClusterPublicKey() []byte             { return g.clusterPubKey }

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

	// Purge NATS stream to ensure no stale messages interfere with the new scenario
	if b := m.bus; b != nil {
		if n, ok := b.(interface{ Purge(context.Context) error }); ok {
			_ = n.Purge(ctx)
		}
	}

	// 1b. Build Gear Deployment Map (name -> rack) for global wire resolution
	gearDeploy := make(map[string]string)
	for _, g := range sc.Gears {
		if target, ok := g.Deploy.(string); ok {
			gearDeploy[g.Name] = target
		}
	}

	// 1c. Resolve port termini (static walk over the wire graph) so gears can
	// bind availability sensing to the right signal source via their context.
	portBindings := computeBindings(sc, m.rackName, gearDeploy, func(gearType string) sdk.TerminusKind {
		man, _ := m.factory.Manifest(gearType)
		return man.Terminus
	})

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

		// Validate the gear config against its manifest schema (ADR 0045), so
		// a bad value or a missing required field fails activation here rather
		// than misbehaving at runtime.
		if err = m.factory.ValidateConfig(gSpec.Type, gSpec.Config); err != nil {
			return fmt.Errorf("gear %s: %w", gSpec.Name, err)
		}

		// 3b. Generate Port IDs (Implicit In/Out for Phase 3)
		var inID, outID uuid.UUID

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
		if inID == uuid.Nil {
			inID = m.idGen.NextEntityID(idgen.EntityPortInput)
		}
		if outID == uuid.Nil {
			outID = m.idGen.NextEntityID(idgen.EntityPortOutput)
		}

		// 3c. Generate or Use Gear ID (Prefer Spec ID if from Mixer)
		var gearID uuid.UUID
		if gSpec.ID != uuid.Nil {
			gearID = gSpec.ID
		} else {
			gearID = m.idGen.NextEntityID(idgen.EntityGear)
		}

		// gearPorts/gearIDs are read on the emit path and the subscribe
		// handler without m.mu, so mutate them under portsMu (their sole guard).
		m.portsMu.Lock()
		m.gearPorts[gSpec.Name] = map[string]uuid.UUID{
			"in":  inID,
			"out": outID,
		}
		m.gearIDs[gSpec.Name] = gearID
		m.portsMu.Unlock()

		// 4. Init Gear
		gCtx := &GearContextImpl{
			ctx:           ctx,
			cfg:           gSpec.Config,
			name:          gSpec.Name,
			machineID:     m.machineID,
			logger:        logger.WithComponent(m.logger(), logger.TypeGear, gSpec.Name),
			idGen:         m.idGen,
			bus:           m.bus,
			mgr:           m.mgr,
			clusterPubKey: m.clusterPubKey,
			emitter:       &portEmitter{m: m, gear: gSpec.Name},
			bindings:      portBindings[gSpec.Name],
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
		toRack, toGear, toPort := parsePortRef(wire.To)

		// An explicit destination rack (rack.gear.port) that is not this rack
		// belongs to another rack's activation.
		if toRack != "" && toRack != m.rackName {
			continue
		}
		targetGear, check := m.activeGears[toGear]
		if !check {
			continue
		}

		// Resolve the input port. Default to "in" when the wire omits it.
		// A PortedGear accepts any named input port and is told the
		// arrival port. A plain gear cannot distinguish inputs, so wiring a
		// role-bearing named input (in_<role>, e.g. in_reply) to one is an
		// ACTIVATION ERROR rather than a silent reroute to "in", which used to
		// mask mis-wired scenarios. Plain "in"/"out" endpoints are unchanged.
		inPort := toPort
		if inPort == "" {
			inPort = "in"
		}
		ported, isPorted := targetGear.(sdk.PortedGear)
		if !isPorted && strings.HasPrefix(inPort, "in_") {
			return fmt.Errorf("wire %s -> %s: gear %q (%T) is not a multi-port gear and cannot receive on named input port %q",
				wire.From, wire.To, toGear, targetGear, inPort)
		}
		portID := m.portID(toGear, inPort)

		// Resolve the 'From' endpoint. An explicit rack (rack.gear.port) wins;
		// otherwise the gear's deploy target; otherwise the local rack. The
		// subject is always the three-level flux.msg.<rack>.<gear>.<port>.
		fromRack, fromGear, fromPort := parsePortRef(wire.From)
		sourceRack := fromRack
		if sourceRack == "" {
			sourceRack = gearDeploy[fromGear]
		}
		if sourceRack == "" {
			sourceRack = m.rackName
		}

		subject := fmt.Sprintf("flux.msg.%s.%s.%s", sourceRack, fromGear, fromPort)

		// Determine Wire Label (ID vs Name)
		wireLabel := fmt.Sprintf("%s -> %s", wire.From, wire.To)
		if wire.ID != uuid.Nil {
			wireLabel = fmt.Sprintf("%x", wire.ID)
		}

		var sub bus.Subscription
		sub, err = m.bus.Subscribe(subject, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
			if msg == nil {
				m.logger().Error("Subscribe Handler Triggered with nil msg", "subject", subject)
				return
			}
			// PROBE HANDLING
			if msg.Flags&fluxmsg.FlagSyncProbe != 0 {
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
				GearID: m.gearID(toGear),
				PortID: portID,
				TSNano: time.Now().UnixNano(),
			})

			// TRACE Logging: Bus Receive (Port In)
			if m.logger().Enabled(ctx, logger.LevelTrace) || m.trace {
				m.logger().Log(ctx, logger.LevelTrace, "Bus Receive",
					"flux_id", msg.FluxID.String(),
					"wire", wireLabel,
					"gear", toGear,
					"port_id", portID.String(),
					"payload_hex", fmt.Sprintf("0x%x", msg.RawPayload),
					"meta", fmt.Sprintf("%v", msg.Metadata),
					"path", formatHops(msg.Path),
				)
			} else if m.debug {
				m.logger().Debug("FluxMsg received",
					"flux_id", msg.FluxID.String(),
					"wire", wireLabel,
				)
			}

			// Delivery to Target Gear
			// Pass the INCOMING CONTEXT (carrying traces) to Process.
			timeoutCtx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()

			// 1. INSTRUMENTATION: Gear Process Span
			tracer := otel.GetTracerProvider().Tracer("fluxrig/runtime")
			spanName := fmt.Sprintf("gear_process %s", toGear)
			processCtx, span := tracer.Start(timeoutCtx, spanName,
				trace.WithAttributes(
					attribute.String("gear", toGear),
					attribute.String("flux_id", msg.FluxID.String()),
				),
			)
			defer span.End()

			start := time.Now()
			// Panic Recovery Middleware (Technical Audit May 2026).
			// A PortedGear is driven via ProcessPort with the arrival
			// port and emits through its PortEmitter, so it produces no return
			// value here (resp stays nil). A plain gear uses Process.
			var resp *fluxmsg.FluxMsg
			var pErr error
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.logger().Error("gear process panic recovered",
							"gear", toGear,
							"panic", r,
							"flux_id", msg.FluxID.String(),
						)
						pErr = fmt.Errorf("gear panic: %v", r)
					}
				}()
				if isPorted {
					pErr = ported.ProcessPort(processCtx, inPort, msg)
				} else {
					resp, pErr = targetGear.Process(processCtx, msg)
				}
			}()
			duration := float64(time.Since(start).Microseconds()) / 1000.0 // Record in ms for consistency with metric name

			// INSTRUMENTATION: Gear Input
			if tm := telemetry.GetMetrics(); tm != nil {
				metricCtx := ctx
				attrs := metric.WithAttributes(
					attribute.String("gear", toGear),
					attribute.String("flux.id", m.gearID(toGear).String()),
					attribute.String("flux.name", toGear),
				)

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

			// A plain gear's Process return goes out the default "out" port.
			// PortedGears emit their own results via the PortEmitter, so resp
			// is nil for them.
			if resp != nil {
				if m.debug {
					m.logger().Debug("FluxMsg processed", "flux_id", resp.FluxID.String())
				}
				_ = m.publishPort(processCtx, toGear, "out", resp)
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
		name, g := name, g // Shadow variables for closure safety

		// The source-gear emit callback is sugar for emitting on the default
		// "out" port. All emit paths (this, Process returns, and the named-port
		// PortEmitter) funnel through publishPort, which is recover-guarded,
		// nil-safe, hop-stamped and instrumented.
		emitFunc := func(msg *fluxmsg.FluxMsg) {
			_ = m.publishPort(context.Background(), name, "out", msg)
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
	// gearPorts/gearIDs are read without m.mu on the emit path; reset under
	// portsMu (their sole guard) so a concurrent emit cannot race the reload.
	m.portsMu.Lock()
	m.gearPorts = make(map[string]map[string]uuid.UUID)
	m.gearIDs = make(map[string]uuid.UUID)
	m.portsMu.Unlock()
	m.hotSubjects = make(map[string]chan struct{})
}

// parsePortRef splits a wire endpoint into (rack, gear, port). Every segment is
// dot-free (port names may not contain dots), so the level is unambiguous by
// segment count:
//
//	"gear.port"       -> ("",   gear, port)  rack implied by the gear's deploy
//	"rack.gear.port"  -> (rack, gear, port)  explicit rack / replica instance
//
// A bare "gear" yields empty rack and port.
func parsePortRef(ref string) (rack, gear, port string) {
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 2:
		return "", parts[0], parts[1]
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		// Malformed (1 or >3 segments); validation rejects these upstream.
		return "", parts[0], strings.Join(parts[1:], ".")
	}
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
		res += fmt.Sprintf("{g:%s, p:%s}", h.GearID, h.PortID)
	}
	res += "]"
	return res
}
