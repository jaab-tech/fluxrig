// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fxamacker/cbor/v2"
	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/lockfile"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ErrReconnect indicates the agent needs to restart its session (e.g. identity change)
var ErrReconnect = fmt.Errorf("reconnect needed")

// RunAgent implements the main loop of the FluxRig Rack Agent.
func RunAgent(cfg *config.RackConfig, logger *slog.Logger, logBuffer *telemetry.BufferHandler) error {
	logger.Debug("Starting Rack Agent", "config", cfg)

	// 0. Ensure DataDir exists and acquire lock
	if err := os.MkdirAll(cfg.Store.Dir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	lockPath := filepath.Join(cfg.Store.Dir, ".lock")
	lock, err := lockfile.Acquire(lockPath)
	if err != nil {
		return fmt.Errorf("INSTANCE ERROR: %v (Is another agent running in %s?)", err, cfg.Store.Dir)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			logger.Error("failed to release instance lock", "error", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := runSession(ctx, cfg, logger, logBuffer)
		if err == ErrReconnect {
			logger.Info("Restarting Session (Identity Changed)")
			// Brief pause to allow connection cleanup propagation
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return err
	}
}

func runSession(ctx context.Context, cfg *config.RackConfig, logger *slog.Logger, logBuffer *telemetry.BufferHandler) error {
	// 0. Try Load Passport (Offline Capability)
	statePath := filepath.Join(cfg.Store.Dir, cfg.Store.StateFile)
	logger.Debug("DEBUG: FLUXRIG_E2E_SCENARIO", "val", os.Getenv("FLUXRIG_E2E_SCENARIO"))
	logger.Debug("Attempting to load Passport", "path", statePath)
	var secret string
	var currentStatus string
	if env, err := pki.LoadStateEnvelope(statePath); err == nil {
		if s, errVer := env.Verify(); errVer == nil {
			logger.Debug("Loaded Cached Passport", "id", s.MachineID, "name", s.Name)
			cfg.Rack.MachineID = s.MachineID
			cfg.Rack.Name = s.Name // Ensure name is loaded for telemetry init
			secret = s.Secret
			currentStatus = s.Status
		} else {
			logger.Error("Failed to verify cached passport", "error", errVer)
		}
	} else {
		logger.Debug("No cached passport found or load failed", "error", err)
	}

	// 1. Parse Timeouts
	connectTimeout, _ := time.ParseDuration(cfg.Rack.Bus.ConnectTimeout)
	reconnectWait, _ := time.ParseDuration(cfg.Rack.Bus.ReconnectWait)
	enrollTimeout, _ := time.ParseDuration(cfg.Rack.EnrollmentTimeout)
	hbInterval, _ := time.ParseDuration(cfg.Rack.HeartbeatInterval)

	// Synchronization Contexts (ADR 0036/ADR 0014)
	// These are now strictly configuration-driven with safe defaults in pkg/config
	cleanupTimeout, errParse := time.ParseDuration(cfg.Rack.CleanupTimeout)
	if errParse != nil {
		return fmt.Errorf("invalid rack.cleanup_timeout: %w", errParse)
	}

	convTimeout, errParse := time.ParseDuration(cfg.Rack.ConvergenceTimeout)
	if errParse != nil {
		return fmt.Errorf("invalid rack.convergence_timeout: %w", errParse)
	}

	handshakeInterval, errParse := time.ParseDuration(cfg.Rack.HandshakeInterval)
	if errParse != nil {
		return fmt.Errorf("invalid rack.handshake_interval: %w", errParse)
	}

	subRetryWait, errParse := time.ParseDuration(cfg.Rack.Bus.SubscriptionRetryWait)
	if errParse != nil {
		return fmt.Errorf("invalid rack.bus.subscription_retry_wait: %w", errParse)
	}

	// 2. Identify effective name for Enrollment and Bus (ADR 0020/ADR 0036)
	helloName := cfg.Rack.Name
	if helloName == "" {
		prefix := cfg.Rack.NamePrefix
		if prefix == "" {
			prefix = "node-"
		}
		helloName = fmt.Sprintf("%spending-%d", prefix, time.Now().UnixNano())
	}

	// 2b. Connect to Bus
	logger.Debug("Connecting to Bus", "url", cfg.Rack.Bus.URL, "name", helloName)
	opts := bus.ConnectOptions{
		Name:                      helloName, // Must match helloName for Mixer topology mapping
		Domain:                    cfg.Rack.Bus.Domain,
		ConnectTimeout:            connectTimeout,
		ReconnectWait:             reconnectWait,
		OperationTimeout:          5 * time.Second,
		SubscriptionRetryWait:     subRetryWait,
		SubscriptionRetryAttempts: cfg.Rack.Bus.SubscriptionRetryAttempts,
		RootCA:                    cfg.Rack.Bus.RootCA,
		InsecureSkipVerify:        cfg.Rack.Bus.InsecureSkipVerify,
	}
	natsBus := bus.NewNatsBus(cfg.Rack.Bus.StreamName)
	busConnected := false

	clientName := helloName

	// Initialize ID Generator
	mID := cfg.Rack.MachineID
	gen, err := idgen.New(mID)
	if err != nil {
		return err
	}

	isSessionActive := false
	runtimeStarted := false
	var lastScenario *registry.Scenario
	var telBusCleanup *bus.NatsBus
	var telShutdown func(context.Context) error
	var rtManager *rt.Manager

	if currentStatus == "active" {
		isSessionActive = true
		runtimeStarted = true
	}

	if errCon := natsBus.Connect(cfg.Rack.Bus.URL, opts); errCon != nil {
		if cfg.Rack.MachineID != 0 {
			logger.Warn("Bus Unavailable. Starting in OFFLINE Mode", "error", errCon)
		} else {
			return fmt.Errorf("bus unavailable and no passport found: %w", errCon)
		}
	} else {
		busConnected = true
		defer natsBus.Close()
		logger.Info("Bus Connected")

		// NOTE: Telemetry initialization is DEFERRED until after passport verification.
		// This ensures we initialize with the correct identity (Name + ID) from the start,
		// avoiding dual MeterProvider issues with stale "pending" identity.
		// See: initTelemetryDeferred() called after passport verification.
	}

	// 2.5 Initialize Spec Manager & Runtime
	opTimeout, _ := time.ParseDuration(cfg.Rack.Bus.OperationTimeout)

	// Spec Manager (Git-backed Registry)
	specStorePath := filepath.Join(cfg.Store.Dir, "store")
	specMgr, errMgr := manager.NewManager(specStorePath)
	if errMgr != nil {
		return fmt.Errorf("failed to init spec manager: %w", errMgr)
	}

	// INSTRUMENTATION: Wrap Bus
	managedBus := telemetry.NewInstrumentedBus(natsBus, nil)

	// Runtime Helper: Re-initializes the routing layer with correct identity
	reinitRuntime := func(nodeID uint16, nodeName string, nodeGen *idgen.IDGenerator) {
		if rtManager != nil {
			logger.Info("Stopping existing Runtime Manager for Identity Rotation")
			rtManager.Shutdown()
		}
		logger.Info("Initializing Runtime Manager", "id", nodeID, "name", nodeName)
		rtManager = rt.NewManager(uint64(nodeID), nodeName, managedBus, nodeGen, specMgr, opTimeout, convTimeout, handshakeInterval)
	}

	// Initial Init (might be 0/pending)
	reinitRuntime(mID, helloName, gen)
	defer func() {
		if rtManager != nil {
			rtManager.Shutdown()
		}
	}()

	// Generate unique session nonce for topic isolation
	nonceBytes := make([]byte, 4)
	_, _ = rand.Read(nonceBytes)
	sessionNonce := hex.EncodeToString(nonceBytes)

	// SCENARIO LOADING: Determined by Mixer after enrollment.

	// 3. Send Hello (If Connected)
	// helloName determined earlier

	hello := &fluxmsg.HelloPayload{
		Name:      helloName,
		Nonce:     sessionNonce,
		Secret:    secret,
		MachineID: mID,
		IP:        "127.0.0.1",
		Port:      8092,
		Version:   version.Version,
		Config:    map[string]any{"rack": cfg.Rack, "logging": cfg.Logging}, // Report runtime config
	}

	if busConnected {
		if errSend := sendHello(managedBus, hello, gen); errSend != nil {
			logger.Error("Failed to send Hello", "error", errSend)
		} else {
			logger.Info("Sent Hello", "name", hello.Name)
		}
	}

	reconnectCh := make(chan struct{}, 1)
	activationCh := make(chan []byte, 1)

	// 3.5 Wait for Passport (State Issuance)
	// We listen for [fluxrig.agent.enrollment.<name>.<nonce>]
	enrollTopic := fmt.Sprintf("fluxrig.agent.enrollment.%s.%s", helloName, sessionNonce)
	logger.Info("Waiting for Passport...", "topic", enrollTopic)

	// Channel to signal graceful shutdown from callbacks
	shutdownCh := make(chan struct{}, 1)

	if busConnected {
		passportCh := make(chan *fluxmsg.HelloResponse, 1)

		sub, errSub := managedBus.Subscribe(enrollTopic, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
			// Parse HelloResponse
			resp, errParse := fluxmsg.ParseHelloResponse(msg.Data)
			if errParse != nil {
				logger.Error("Failed to parse enrollment response", "error", errParse)
				return
			}
			logger.Info("Received Enrollment Response", "message", resp.Message, "status", resp.Status)

			select {
			case passportCh <- resp:
			default:
			}
		})
		if errSub != nil {
			logger.Error("Failed to subscribe to enrollment", "error", errSub)
			return errSub
		}

		defer func() { _ = sub.Unsubscribe() }()

		// Wait with timeout
		select {
		case resp := <-passportCh:
			// Process Passport if present
			if len(resp.Passport) > 0 {
				env := pki.StateEnvelope{}
				if errUnmarshal := cbor.Unmarshal(resp.Passport, &env); errUnmarshal != nil {
					logger.Error("Failed to unmarshal passport envelope", "error", errUnmarshal)
					return errUnmarshal
				}

				// Re-verify
				state, errVerify := env.Verify()
				if errVerify != nil {
					logger.Error("Passport invalid", "error", errVerify)
					return errVerify
				}

				logger.Info("Passport Verified", "id", state.MachineID, "name", state.Name)
				// Save path already calculated as statePath
				if errMk := os.MkdirAll(cfg.Store.Dir, 0750); errMk != nil {
					return errMk
				}

				if errSave := env.Save(statePath); errSave != nil {
					logger.Error("Failed to save state.flux", "error", errSave)
					return errSave
				}
				logger.Info("Passport Saved", "path", statePath)
				mID = state.MachineID

				if resp.Status == "active" {
					// INITIALIZE TELEMETRY WITH CONFIRMED IDENTITY
					// Now we have the real identity (Name + ID) - this is the first and only telemetry init
					newGen, _ := idgen.New(state.MachineID)
					newEntityID := newGen.NewEntityID(idgen.EntityRack, 0)

					telCfg := telemetry.Config{
						ServiceName:         cfg.Telemetry.ServiceName,
						ServiceVersion:      version.Version,
						EntityID:            newEntityID,
						EntityName:          state.Name,
						Component:           string(loggerPkg.TypeRack),
						BatchIntervalString: cfg.Telemetry.BatchInterval,
						BaseSubject:         cfg.Telemetry.BaseSubject,
						MaxBatchSize:        cfg.Telemetry.MaxBatchSize,
						Logging:             cfg.Logging,
						Store:               cfg.Store,
						Throttling:          cfg.Logging.Throttling,
						StdoutEnabled:       true,
						StdoutLevel:         "info",
						Metrics: telemetry.MetricsConfig{
							HostEnabled:    cfg.Telemetry.Metrics.HostEnabled,
							RuntimeEnabled: cfg.Telemetry.Metrics.RuntimeEnabled,
							BentoEnabled:   cfg.Telemetry.Metrics.BentoEnabled,
						},
					}

					// Override Level if FLUXRIG_TRACE is set
					if os.Getenv("FLUXRIG_TRACE") == "true" || os.Getenv("FLUXRIG_TRACE") == "1" {
						telCfg.Logging.Level = "trace"
					}

					// Initialize dedicated Telemetry Bus (Isolation)
					telBus := bus.NewNatsBus("flux-telemetry") // Bound to dedicated JS stream
					if errBus := telBus.Connect(cfg.Rack.Bus.URL, bus.ConnectOptions{
						Name:           clientName + "-telemetry",
						ConnectTimeout: connectTimeout,
						ReconnectWait:  reconnectWait,
						Domain:         cfg.Rack.Bus.Domain, // Usually same domain
						RootCA:         cfg.Rack.Bus.RootCA,
					}); errBus != nil {
						logger.Warn("Failed to connect telemetry bus", "error", errBus)
						// Fallback to shared bus if needed? No, fail fast for isolation.
					} else {
						defer telBus.Close()
					}

					if os.Getenv("FLUXRIG_DISABLE_TELEMETRY") != "true" {
						sDown, errInit := telemetry.Init(context.Background(), telCfg, telBus, logBuffer, newGen)
						if errInit != nil {
							logger.Warn("Failed to init telemetry", "error", errInit)
						} else {
							telShutdown = sDown
							telBusCleanup = telBus
							// Update logger to use OTel Bridge
							logger = slog.Default().With(
								"component", string(loggerPkg.TypeRack),
								"name", state.Name,
							)
							slog.SetDefault(logger)
							// Re-initialize Runtime with correct identity
							reinitRuntime(state.MachineID, state.Name, newGen)
							isSessionActive = true
							runtimeStarted = true
						}
					}
				} else {
					logger.Info("Rack is PENDING adoption. Deferring telemetry and runtime.")
					isSessionActive = false
				}
			} else {
				// No passport? Should not happen in strict mode implementation
				logger.Warn("No passport received in HelloResponse?")
			}

			// STATUS HANDLING
			newStatus := resp.Status
			logger.Info("Current Status", "status", newStatus)

		case <-time.After(enrollTimeout):
			if cfg.Rack.MachineID == 0 {
				logger.Warn("Enrollment Timeout. No Passport received.")
				// NOTE: In deferred adoption mode, we no longer initialize telemetry for pending fallbacks.
				// The Rack remains silent until officially adopted.
				fallbackName := cfg.Rack.Name
				if fallbackName == "" {
					fallbackName = "pending"
				}
				logger.Info("Rack enrollment timed out. Waiting for manual adoption.", "name", fallbackName)
			} else {
				logger.Info("Resuming Session (Offline/Timeout)", "id", cfg.Rack.MachineID)
				// Resuming from saved passport - init telemetry with saved identity
				entityID := gen.NewEntityID(idgen.EntityRack, 0)

				telCfg := telemetry.Config{
					ServiceName:         cfg.Telemetry.ServiceName,
					ServiceVersion:      version.Version,
					EntityID:            entityID,
					EntityName:          cfg.Rack.Name,
					Component:           string(loggerPkg.TypeRack),
					BatchIntervalString: cfg.Telemetry.BatchInterval,
					BaseSubject:         cfg.Telemetry.BaseSubject,
					MaxBatchSize:        cfg.Telemetry.MaxBatchSize,
					Logging:             cfg.Logging,
					Store:               cfg.Store,
					Throttling:          cfg.Logging.Throttling,
					StdoutEnabled:       true,
					StdoutLevel:         "info",
					Metrics: telemetry.MetricsConfig{
						HostEnabled:    cfg.Telemetry.Metrics.HostEnabled,
						RuntimeEnabled: cfg.Telemetry.Metrics.RuntimeEnabled,
						BentoEnabled:   cfg.Telemetry.Metrics.BentoEnabled,
					},
				}

				if os.Getenv("FLUXRIG_DISABLE_TELEMETRY") != "true" {
					sDown, errInit := telemetry.Init(context.Background(), telCfg, natsBus, logBuffer, gen)
					if errInit != nil {
						logger.Warn("Failed to init telemetry (resume)", "error", errInit)
					} else {
						telShutdown = sDown
						// 4. Initialize Data Plane
						rtManager = rt.NewManager(uint64(cfg.Rack.MachineID), cfg.Rack.Name, natsBus, gen, specMgr, opTimeout, convTimeout, handshakeInterval)

						if errStart := rtManager.Start(); errStart != nil {
							logger.Error("Failed to start runtime (resume)", "error", errStart)
						}
						logger = slog.Default().With("component", string(loggerPkg.TypeRack), "name", cfg.Rack.Name)
						slog.SetDefault(logger)
						logger.Info("Telemetry Initialized (resume)", "id", cfg.Rack.MachineID, "name", cfg.Rack.Name)
					}
				}
			}
		}

		// ALWAYS Subscribe to Runtime Topics (Scenario & Notify)
		// We do this after enrollment attempt to ensure we have the final Identity (mID, helloName)
		// If enrollment updated them, good. If timeout, we use what we have.

		// 1. Notifications (Status/Commands)
		if mID > 0 {
			notifyTopic := fmt.Sprintf("fluxrig.agent.notify.%d", mID)
			_, err = managedBus.Subscribe(notifyTopic, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
				hbResp, errParse := fluxmsg.ParseHeartbeatResponse(msg.Data)
				if errParse != nil {
					return
				}
				// Handle Status/Command (Logic shared with enrollment response path?)
				// Simplified: Just log for now, full status sync is complex.
				if hbResp.Command != "" {
					logger.Info("Received Command", "cmd", hbResp.Command)
					if strings.HasPrefix(hbResp.Command, "set_log_level:") {
						level := strings.TrimPrefix(hbResp.Command, "set_log_level:")
						loggerPkg.SetLevel(logger, level)
						logger.Info("Log Level Dynamically Updated", "level", level)
					}
					if hbResp.Command == "agent:shutdown" {
						logger.Info("Received Shutdown Command via Heartbeat - Initiating Graceful Drain")
						// 1. Drain Gears (Stop accepting new work, finish pending)
						drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer drainCancel()
						if errDrain := rtManager.Drain(drainCtx); errDrain != nil {
							logger.Error("Drain Error", "error", errDrain)
						} else {
							logger.Info("All Gears Drained Successfully")
						}

						// 2. Signal Main Loop to Exit (triggers defer cleanup)
						select {
						case shutdownCh <- struct{}{}:
						default:
						}
						return
					}
				}
				// Handle Passport Update (Adoption)
				if len(hbResp.Passport) > 0 {
					env := pki.StateEnvelope{}
					if errAdopt := cbor.Unmarshal(hbResp.Passport, &env); errAdopt != nil {
						logger.Error("Adoption: Failed to unmarshal passport envelope", "error", errAdopt)
					} else {
						// Verify
						state, errVerify := env.Verify()
						if errVerify != nil {
							logger.Error("Adoption: Passport invalid", "error", errVerify)
						} else {
							// Save
							statePath := filepath.Join(cfg.Store.Dir, cfg.Store.StateFile)
							if errSave := env.Save(statePath); errSave != nil {
								logger.Error("Adoption: Failed to save state.flux", "error", errSave)
							} else {
								logger.Info("Received Updated Passport via Heartbeat", "id", state.MachineID, "name", state.Name)
								logger.Info("Adoption: Passport Saved", "path", statePath)

								// Check Status Change
								if currentStatus != "" && state.Status != currentStatus {
									logger.Info("Status Changed", "old", currentStatus, "new", state.Status)
									currentStatus = state.Status
								}

								// RECONNECT LOGIC:
								// We must reconnect if:
								// 1. Identity name/id changed (Standard rotation)
								// 2. We were previously PENDING and are now ACTIVE (Promotion)
								shouldReconnect := false
								if state.Name != cfg.Rack.Name || state.MachineID != cfg.Rack.MachineID {
									logger.Info("Adoption: Identity Updated - Restarting Session for Telemetry Rotation", "old_name", cfg.Rack.Name, "new_name", state.Name)
									shouldReconnect = true
								}

								if shouldReconnect {
									// Update config for next session
									cfg.Rack.Name = state.Name
									cfg.Rack.MachineID = state.MachineID

									// Signal reconnect
									select {
									case reconnectCh <- struct{}{}:
									default:
									}
								} else if state.Status == "active" && !isSessionActive {
									logger.Info("Promotion: Rack promoted to ACTIVE - Triggering In-Place Activation")
									select {
									case activationCh <- hbResp.Passport:
									default:
									}
								} else {
									logger.Debug("Adoption: Session already active and identity matched, no rotation needed")
								}
							}
						}
					}
				}
			})
			if err != nil {
				logger.Error("Failed to subscribe to notify", "error", err)
			} else {
				logger.Info("Listening for notifications", "topic", notifyTopic)
				// defer func() { _ = notifSub.Unsubscribe() }() // Lifetime of agent
			}
		}

		// 2. Scenario Updates
		// Topic: fluxrig.rack.{name}.scenario
		scenarioTopic := fluxmsg.SubjectScenarioPrefix + helloName + fluxmsg.SubjectScenarioSuffix
		_, err = managedBus.Subscribe(scenarioTopic, func(ctx context.Context, msg *fluxmsg.FluxMsg) {
			payload, errParse := fluxmsg.ParseScenarioPayload(msg.Data)
			if errParse != nil {
				logger.Error("Failed to parse scenario payload", "error", errParse)
				return
			}

			logger.Info("Received Scenario from Mixer",
				"name", payload.Name,
				"version", payload.Version,
				"rack", payload.RackName,
			)

			// Parse and apply the scenario
			var sc registry.Scenario
			if errParseSc := yaml.Unmarshal(payload.Scenario, &sc); errParseSc != nil {
				logger.Error("Failed to unmarshal scenario from payload", "error", errParseSc)
				return
			}

			if isSessionActive {
				if errApply := rtManager.ApplyScenario(ctx, &sc); errApply != nil {
					logger.Error("Failed to apply scenario", "error", errApply)
					return
				}
				logger.Info("Scenario Applied Successfully", "version", payload.Version)
				runtimeStarted = true
			} else {
				logger.Info("Rack Pending: Scenario Cached - Waiting for Promotion", "version", payload.Version)
				lastScenario = &sc
			}
		})
		if err != nil {
			logger.Error("Failed to subscribe to scenario topic", "error", err)
		} else {
			logger.Info("Listening for scenario updates", "topic", scenarioTopic)
			// defer func() { _ = scenarioSub.Unsubscribe() }()
		}

	} else {
		logger.Info("Skipping Enrollment (Offline)")
	}

	// 4. Heartbeat Loop
	// Initial heartbeat
	if busConnected {
		if errHBInit := sendHeartbeat(ctx, managedBus, mID, cfg, gen); errHBInit != nil {
			logger.Warn("Failed to send initial heartbeat", "error", errHBInit)
		}
	}

	logger.With("component", "RACK", "name", cfg.Rack.Name).Info("Agent Running", "heartbeat", hbInterval)

	// 4.1 Setup Ticker for Heartbeats
	ticker := time.NewTicker(hbInterval)
	defer ticker.Stop()

	// 4.2 Local Cleanup Handler (Manual trigger for in-loop telemetry re-init)
	cleanupTelemetry := func() {
		if telShutdown != nil {
			sctx, c := context.WithTimeout(context.Background(), cleanupTimeout)
			defer c()
			_ = telShutdown(sctx)
			telShutdown = nil
		}
		if telBusCleanup != nil {
			telBusCleanup.Close()
			telBusCleanup = nil
		}
	}
	defer cleanupTelemetry()

	// Block forever
	// Heartbeat Loop (with reconnect handling)
	for {
		select {
		case <-ctx.Done():
			logger.Info("Signal Received. Shutting down Agent...", "signal", "SIGTERM/SIGINT")
			return nil
		case <-shutdownCh:
			logger.Info("Shutdown Command Received. Exiting Main Loop.")
			return nil
		case <-reconnectCh:
			return ErrReconnect
		case passport := <-activationCh:
			// IN-PLACE PROMOTION
			if isSessionActive && runtimeStarted {
				continue
			}
			logger.Info("Executing In-Place Promotion")
			cleanupTelemetry()

			// Re-marshal passport to env for state extraction
			env := pki.StateEnvelope{}
			_ = cbor.Unmarshal(passport, &env)
			state, _ := env.Verify()

			// Initialize Telemetry
			newGen, _ := idgen.New(state.MachineID)
			newEntityID := newGen.NewEntityID(idgen.EntityRack, 0)
			telCfg := telemetry.Config{
				ServiceName:         cfg.Telemetry.ServiceName,
				ServiceVersion:      version.Version,
				EntityID:            newEntityID,
				EntityName:          state.Name,
				Component:           string(loggerPkg.TypeRack),
				BatchIntervalString: cfg.Telemetry.BatchInterval,
				BaseSubject:         cfg.Telemetry.BaseSubject,
				MaxBatchSize:        cfg.Telemetry.MaxBatchSize,
				Logging:             cfg.Logging,
				Store:               cfg.Store,
				Throttling:          cfg.Logging.Throttling,
				StdoutEnabled:       true,
				StdoutLevel:         "info",
				Metrics: telemetry.MetricsConfig{
					HostEnabled:    cfg.Telemetry.Metrics.HostEnabled,
					RuntimeEnabled: cfg.Telemetry.Metrics.RuntimeEnabled,
					BentoEnabled:   cfg.Telemetry.Metrics.BentoEnabled,
				},
			}
			if os.Getenv("FLUXRIG_TRACE") == "true" || os.Getenv("FLUXRIG_TRACE") == "1" {
				telCfg.Logging.Level = "trace"
			}
			telBus := bus.NewNatsBus("flux-telemetry")
			_ = telBus.Connect(cfg.Rack.Bus.URL, bus.ConnectOptions{
				Name:           clientName + "-telemetry",
				ConnectTimeout: connectTimeout,
				ReconnectWait:  reconnectWait,
				Domain:         cfg.Rack.Bus.Domain,
				RootCA:         cfg.Rack.Bus.RootCA,
			})

			sDown, _ := telemetry.Init(context.Background(), telCfg, telBus, logBuffer, newGen)
			telShutdown = sDown
			telBusCleanup = telBus

			// MANDATORY Telemetry Handshake (ADR 0036)
			if errTel := telemetry.VerifyConnectivity(context.Background(), telBus, state.Name, convTimeout, handshakeInterval); errTel != nil {
				logger.Error("Telemetry convergence failure. Aborting promotion.", "error", errTel)
				cleanupTelemetry()
				continue // Retry on next heartbeat/activation
			}

			// Re-initialize Runtime
			reinitRuntime(state.MachineID, state.Name, newGen)

			logger = slog.Default().With("component", string(loggerPkg.TypeRack), "name", state.Name)
			slog.SetDefault(logger)
			logger.Info("In-Place Telemetry & Runtime Initialized")
			isSessionActive = true

			// Apply Cached Scenario (with mandatory convergence handshake)
			if lastScenario != nil {
				logger.Info("Applying Cached Scenario upon Promotion", "name", lastScenario.Meta.Name)
				if errApply := rtManager.ApplyScenario(ctx, lastScenario); errApply != nil {
					logger.Error("Data-plane convergence failure. Aborting promotion.", "error", errApply)
					isSessionActive = false
					runtimeStarted = false
					continue
				}
				runtimeStarted = true
			}

			logger.Info("In-Place Promotion Complete. Path is Hot.")
			isSessionActive = true
		case <-ticker.C:
			if busConnected {
				// We use current mID (might have changed after passport load)
				if err := sendHeartbeat(ctx, managedBus, mID, cfg, gen); err != nil {
					logger.Warn("Failed to send heartbeat", "error", err)
				} else {
					logger.Debug("Sent Heartbeat", "mid", mID)
					// INSTRUMENTATION: Heartbeat Sent
					if ms := telemetry.GetMetrics(); ms != nil {
						ms.HeartbeatsSent.Add(ctx, 1)
						logger.Debug("Incremented heartbeats_sent metric")
					}
				}
			}
		}
	}
}

func sendHello(b bus.Bus, p *fluxmsg.HelloPayload, gen *idgen.IDGenerator) error {
	data, err := p.ToData()
	if err != nil {
		return err
	}

	msg := fluxmsg.New()
	msg.Data = data
	// Need to set src_id if we have it
	msg.SrcGearID = uint64(p.MachineID)

	// Set FluxID
	id, _ := gen.NextFluxID()
	msg.FluxID = id

	// Hello is root span usually? Use Background or passed ctx?
	// Note: sendHello signature has no context. We can update it or use Background.
	// Since it's helper, let's use Background for now.
	return b.Publish(context.Background(), fluxmsg.SubjectAgentHello, msg)
}

func sendHeartbeat(ctx context.Context, b bus.Bus, mid uint16, cfg *config.RackConfig, gen *idgen.IDGenerator) error {
	stats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
	}

	p := &fluxmsg.HeartbeatPayload{
		MachineID: mid,
		Stats:     stats,
		Config:    map[string]any{"rack": cfg.Rack, "logging": cfg.Logging},
	}

	data, err := p.ToData()
	if err != nil {
		return err
	}

	msg := fluxmsg.New()
	id, _ := gen.NextFluxID()
	msg.FluxID = id
	msg.Data = data
	msg.SrcGearID = uint64(mid)

	return b.Publish(ctx, fluxmsg.SubjectAgentHeartbeat, msg)
}

// Config Path for Flag
var configPath string

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the FluxRig Agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		var cfg *config.RackConfig
		var err error

		// 1. Load from Config File (if provided or default exists)
		if configPath != "" {
			cfg, err = config.LoadRack(configPath)
		} else {
			// Fallback to Env-based minimal load if no config file explicitly passed
			cfg, err = config.LoadRack("")
		}

		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		logger := setupLogger(cfg)
		// Buffer pre-telemetry logs for 1-to-1 parity
		bufHandler := telemetry.NewBufferHandler(logger.Handler())
		bufLogger := slog.New(bufHandler)
		slog.SetDefault(bufLogger)

		return RunAgent(cfg, bufLogger, bufHandler)
	},
}

func setupLogger(cfg *config.RackConfig) *slog.Logger {
	level := cfg.Logging.Level
	// Env Override (Common convention)
	if os.Getenv("FLUXRIG_TRACE") != "" {
		level = "trace"
	} else if os.Getenv("FLUXRIG_DEBUG") != "" {
		level = "debug"
	}
	// Fallback to defaults handled in logger.New or parseLevel

	// Standardized Logger Setup
	return loggerPkg.New(loggerPkg.Config{
		Level:      level,
		EntityType: loggerPkg.TypeRack,
		Name:       "rack-agent",
		Writer:     os.Stdout,
	})
}

func init() {
	runCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to configuration file")
	rootCmd.AddCommand(runCmd)
}
