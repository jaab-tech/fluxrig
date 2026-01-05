package commands

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/lockfile"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	rt "github.com/jaab-tech/fluxrig/pkg/runtime"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/spf13/cobra"
	"github.com/vmihailenco/msgpack/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"gopkg.in/yaml.v3"
)

// ErrReconnect indicates the agent needs to restart its session (e.g. identity change)
var ErrReconnect = fmt.Errorf("reconnect needed")

// RunAgent implements the main loop of the FluxRig Rack Agent.
func RunAgent(cfg *config.RackConfig, logger *slog.Logger, logBuffer *telemetry.BufferHandler) error {
	logger.Info("Starting Rack Agent", "config", cfg)

	// 0. Ensure DataDir exists and acquire lock
	if err := os.MkdirAll(cfg.Store.Dir, 0755); err != nil {
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
	logger.Info("DEBUG: FLUXRIG_E2E_SCENARIO", "val", os.Getenv("FLUXRIG_E2E_SCENARIO"))
	logger.Info("Attempting to load Passport", "path", statePath)
	var secret string
	var currentStatus string
	if env, err := pki.LoadStateEnvelope(statePath); err == nil {
		if s, err := env.Verify(); err == nil {
			logger.Info("Loaded Cached Passport", "id", s.MachineID, "name", s.Name)
			cfg.Rack.MachineID = s.MachineID
			cfg.Rack.Name = s.Name // Ensure name is loaded for telemetry init
			secret = s.Secret
			currentStatus = s.Status
		} else {
			logger.Error("Failed to verify cached passport", "error", err)
		}
	} else {
		logger.Info("No cached passport found or load failed", "error", err)
	}

	// 1. Parse Timeouts
	connectTimeout, _ := time.ParseDuration(cfg.Rack.Bus.ConnectTimeout)
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	reconnectWait, _ := time.ParseDuration(cfg.Rack.Bus.ReconnectWait)
	if reconnectWait == 0 {
		reconnectWait = 1 * time.Second
	}
	enrollTimeout, _ := time.ParseDuration(cfg.Rack.EnrollmentTimeout)
	if enrollTimeout == 0 {
		enrollTimeout = 2 * time.Second
	}
	hbInterval, _ := time.ParseDuration(cfg.Rack.HeartbeatInterval)
	if hbInterval == 0 {
		hbInterval = 30 * time.Second
	}

	// 2. Connect to Bus
	logger.Info("Connecting to Bus", "url", cfg.Rack.Bus.URL)
	natsBus := bus.NewNatsBus(cfg.Rack.Bus.StreamName)
	busConnected := false

	// Determine effective name
	helloName := cfg.Rack.Name
	if helloName == "" {
		prefix := cfg.Rack.NamePrefix
		if prefix == "" {
			prefix = "node-"
		}
		helloName = fmt.Sprintf("%spending-%d", prefix, time.Now().UnixNano())
	}

	clientName := helloName

	// Initialize ID Generator
	mID := cfg.Rack.MachineID
	gen, err := idgen.New(mID)
	if err != nil {
		return err
	}

	if err := natsBus.Connect(cfg.Rack.Bus.URL, clientName, connectTimeout, reconnectWait); err != nil {
		if cfg.Rack.MachineID != 0 {
			logger.Warn("Bus Unavailable. Starting in OFFLINE Mode", "error", err)
		} else {
			return fmt.Errorf("bus unavailable and no passport found: %w", err)
		}
	} else {
		busConnected = true
		defer natsBus.Close()
		logger.Info("Bus Connected")

		// 2b. Initialize Telemetry (Zero-Config / Embedded)
		// Rack uses default batch settings (5s, 512).
		// Initial Identity: ID="0", Name="pending" (unless loaded from passport or static ID)

		initialName := "pending"
		if cfg.Rack.MachineID > 0 && cfg.Rack.Name != "" {
			initialName = cfg.Rack.Name
		}

		// gen created above
		entityID := gen.NewEntityID(idgen.EntityRack, 0)

		telCfg := telemetry.Config{
			ServiceName:         "flux-rack",
			ServiceVersion:      version.Version,
			EntityID:            entityID,
			EntityName:          initialName,
			BatchIntervalString: "5s",
			BaseSubject:         "flux.telemetry",
			MaxBatchSize:        512,
			Logging:             cfg.Logging,
			Store:               cfg.Store,
			Throttling:          cfg.Logging.Throttling,
		}

		// Override Level if FLUXRIG_TRACE is set
		if os.Getenv("FLUXRIG_TRACE") == "true" || os.Getenv("FLUXRIG_TRACE") == "1" {
			telCfg.Logging.Level = "trace"
			// WAL level follows global level
		}

		if os.Getenv("FLUXRIG_DISABLE_TELEMETRY") == "true" {
			logger.Info("Telemetry Disabled by Env Var")
		} else {
			shutdownTel, err := telemetry.Init(context.Background(), telCfg, natsBus, logBuffer, gen)
			if err != nil {
				logger.Warn("Failed to init telemetry", "error", err)
			} else {
				defer func() {
					if err := shutdownTel(context.Background()); err != nil {
						logger.Error("Telemetry shutdown error", "error", err)
					}
				}()
				// REFRESH LOGGER: telemetry.Init sets the global default logger (OTel Bridge).
				// We must update our local logger to point to this new default so subsequent
				// 6. Update Logger to use OTel Bridge (slog.Default() was updated by telemetry.Init)
				// We also inject the RACK component and Agent Name so they appear in OTel attributes.
				logger = slog.Default().With(
					"component", string(loggerPkg.TypeRack),
					"name", initialName,
				)
				slog.SetDefault(logger)
				logger.Info("Telemetry Initialized")
			}
		}
	}

	// 2. Initialize ID Generator
	// 2. Initialize ID Generator (Already done above)
	// mID := cfg.Rack.MachineID <-- Already defined
	// gen, err := idgen.New(mID) <-- Already done
	// if err != nil { return err }
	_ = gen // unused for now except for generating trace IDs if we wanted

	// 2.5 Initialize Runtime Manager (Rack Engine)
	rtManager := rt.NewManager(logger, natsBus, gen, uint64(mID), clientName)
	defer rtManager.Shutdown()

	// Scenario Loading:
	// 1. If connected: Rack subscribes to "fluxrig.rack.{name}.scenario" and receives from Mixer
	// 2. If offline: Rack loads from state.flux (cached scenario signed by Mixer)
	// The FLUXRIG_E2E_SCENARIO env var hack has been removed - scenarios ONLY come from Mixer

	// 3. Send Hello (If Connected)
	// helloName determined earlier

	hello := &fluxmsg.HelloPayload{
		Name:      helloName,
		Secret:    secret,
		MachineID: mID,
		IP:        "127.0.0.1",
		Port:      8092,
		Version:   version.Version,
		Config:    map[string]any{"rack": cfg.Rack, "logging": cfg.Logging}, // Report runtime config
	}

	if busConnected {
		if err := sendHello(natsBus, hello, gen); err != nil {
			logger.Error("Failed to send Hello", "error", err)
		} else {
			logger.Info("Sent Hello", "name", hello.Name)
		}
	}

	reconnectCh := make(chan struct{}, 1)

	// 3.5 Wait for Passport (State Issuance)
	// We listen for [fluxrig.agent.enrollment.<name>]
	enrollTopic := fmt.Sprintf("fluxrig.agent.enrollment.%s", helloName)
	logger.Info("Waiting for Passport...", "topic", enrollTopic)

	if busConnected {
		passportCh := make(chan *fluxmsg.HelloResponse, 1)

		sub, err := natsBus.Subscribe(enrollTopic, func(msg *fluxmsg.FluxMsg) {
			// Parse HelloResponse
			resp, err := fluxmsg.ParseHelloResponse(msg.Data)
			if err != nil {
				logger.Error("Failed to parse enrollment response", "error", err)
				return
			}
			logger.Info(resp.Message, "status", resp.Status)

			select {
			case passportCh <- resp:
			default:
			}
		})
		if err != nil {
			logger.Error("Failed to subscribe to enrollment", "error", err)
			return err
		}

		defer func() { _ = sub.Unsubscribe() }()

		// Wait with timeout
		select {
		case resp := <-passportCh:
			// Process Passport if present
			if len(resp.Passport) > 0 {
				env := pki.StateEnvelope{}
				if err := msgpack.Unmarshal(resp.Passport, &env); err != nil {
					logger.Error("Failed to unmarshal passport envelope", "error", err)
					return err
				}

				// Re-verify
				state, err := env.Verify()
				if err != nil {
					logger.Error("Passport invalid", "error", err)
					return err
				}

				logger.Info("Passport Verified", "id", state.MachineID, "name", state.Name)
				// Save path already calculated as statePath
				if err := os.MkdirAll(cfg.Store.Dir, 0755); err != nil {
					return err
				}

				if err := env.Save(statePath); err != nil {
					logger.Error("Failed to save state.flux", "error", err)
					return err
				}
				logger.Info("Passport Saved", "path", statePath)
				mID = state.MachineID

				// TRIGGER TELEMETRY UPDATE
				// Now we have the real identity (Name + ID)
				newGen, _ := idgen.New(state.MachineID)
				newEntityID := newGen.NewEntityID(idgen.EntityRack, 0)

				if err := telemetry.UpdateIdentity(context.Background(), newEntityID, state.Name); err != nil {
					logger.Warn("Failed to update telemetry identity", "error", err)
				}

				// Refresh local logger reference IMMEDIATELY so we can log the success event with the new identity
				logger = slog.Default()
				logger.Info("Telemetry Identity Updated", "id", state.MachineID, "name", state.Name)
			} else {
				// No passport? Should not happen in strict mode implementation
				logger.Warn("No passport received in HelloResponse?")
			}

			// STATUS HANDLING
			currentStatus := resp.Status
			logger.Info("Current Status", "status", currentStatus)

		case <-time.After(enrollTimeout):
			if cfg.Rack.MachineID == 0 {
				logger.Warn("Enrollment Timeout. No Passport received.")
				// Fallback or retry? For now continue as pending.
			} else {
				logger.Info("Resuming Session (Offline/Timeout)", "id", cfg.Rack.MachineID)
			}
		}

		// ALWAYS Subscribe to Runtime Topics (Scenario & Notify)
		// We do this after enrollment attempt to ensure we have the final Identity (mID, helloName)
		// If enrollment updated them, good. If timeout, we use what we have.

		// 1. Notifications (Status/Commands)
		if mID > 0 {
			notifyTopic := fmt.Sprintf("fluxrig.agent.notify.%d", mID)
			_, err = natsBus.Subscribe(notifyTopic, func(msg *fluxmsg.FluxMsg) {
				hbResp, err := fluxmsg.ParseHeartbeatResponse(msg.Data)
				if err != nil {
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
				}
				// Handle Passport Update (Adoption)
				if len(hbResp.Passport) > 0 {
					env := pki.StateEnvelope{}
					if err := msgpack.Unmarshal(hbResp.Passport, &env); err != nil {
						logger.Error("Adoption: Failed to unmarshal passport envelope", "error", err)
					} else {
						// Verify
						state, err := env.Verify()
						if err != nil {
							logger.Error("Adoption: Passport invalid", "error", err)
						} else {
							// Save
							statePath := filepath.Join(cfg.Store.Dir, cfg.Store.StateFile)
							if err := env.Save(statePath); err != nil {
								logger.Error("Adoption: Failed to save state.flux", "error", err)
							} else {
								logger.Info("Received Updated Passport via Heartbeat", "id", state.MachineID, "name", state.Name)
								logger.Info("Adoption: Passport Saved", "path", statePath)

								// Check Status Change
								if currentStatus != "" && state.Status != currentStatus {
									logger.Info("Status Changed", "old", currentStatus, "new", state.Status)
									currentStatus = state.Status
								}

								// Update Telemetry Identity
								newGen, _ := idgen.New(state.MachineID)
								newEntityID := newGen.NewEntityID(idgen.EntityRack, 0)
								if err := telemetry.UpdateIdentity(context.Background(), newEntityID, state.Name); err != nil {
									logger.Warn("Adoption: Failed to update telemetry identity", "error", err)
								}
								// Update local logger
								logger = slog.Default()
								logger.Info("Adoption: Identity Updated", "name", state.Name)
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
		_, err = natsBus.Subscribe(scenarioTopic, func(msg *fluxmsg.FluxMsg) {
			payload, err := fluxmsg.ParseScenarioPayload(msg.Data)
			if err != nil {
				logger.Error("Failed to parse scenario payload", "error", err)
				return
			}

			logger.Info("Received Scenario from Mixer",
				"name", payload.Name,
				"version", payload.Version,
				"rack", payload.RackName,
			)

			// Parse and apply the scenario
			var sc registry.Scenario
			if err := yaml.Unmarshal(payload.Scenario, &sc); err != nil {
				logger.Error("Failed to parse scenario YAML", "error", err)
				return
			}

			if err := rtManager.ApplyScenario(context.Background(), &sc); err != nil {
				logger.Error("Failed to apply scenario", "error", err)
				return
			}

			logger.Info("Scenario Applied Successfully", "version", payload.Version)
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
		if err := sendHeartbeat(ctx, natsBus, mID, cfg, gen); err != nil {
			logger.Warn("Failed to send initial heartbeat", "error", err)
		}
	}

	logger.With("component", "RACK", "name", cfg.Rack.Name).Info("Agent Running", "heartbeat", hbInterval)

	ticker := time.NewTicker(hbInterval)
	defer ticker.Stop()

	// Metrics
	meter := otel.Meter("flux-rack")
	hbCounter, err := meter.Int64Counter("heartbeats_sent", metric.WithDescription("Number of heartbeats sent"))
	if err != nil {
		logger.Warn("Failed to create heartbeat counter", "error", err)
	} else {
		logger.Info("Heartbeat metric counter created", "name", "heartbeats_sent")
	}

	// Block forever
	// Heartbeat Loop (with reconnect handling)
	for {
		select {
		case <-ctx.Done():
			logger.Info("Signal Received. Shutting down Agent...", "signal", "SIGTERM/SIGINT")
			return nil
		case <-reconnectCh:
			return ErrReconnect
		case <-ticker.C:
			if busConnected {
				// We use current mID (might have changed after passport load)
				if err := sendHeartbeat(ctx, natsBus, mID, cfg, gen); err != nil {
					logger.Warn("Failed to send heartbeat", "error", err)
				} else {
					logger.Debug("Sent Heartbeat", "mid", mID)
					if hbCounter != nil {
						hbCounter.Add(context.Background(), 1)
						logger.Debug("Incremented heartbeats_sent metric")
					}
				}
			}
		}
	}
}

func sendHello(b *bus.NatsBus, p *fluxmsg.HelloPayload, gen *idgen.IDGenerator) error {
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

	return b.Publish(fluxmsg.SubjectAgentHello, msg)
}

func sendHeartbeat(ctx context.Context, b *bus.NatsBus, mid uint16, cfg *config.RackConfig, gen *idgen.IDGenerator) error {
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

	return b.PublishWithContext(ctx, fluxmsg.SubjectAgentHeartbeat, msg)
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
