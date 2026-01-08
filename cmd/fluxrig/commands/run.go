// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	logger.Debug("Connecting to Bus", "url", cfg.Rack.Bus.URL)
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

	if errCon := natsBus.Connect(cfg.Rack.Bus.URL, bus.ConnectOptions{
		Name:           clientName,
		ConnectTimeout: connectTimeout,
		ReconnectWait:  reconnectWait,
		RootCA:         cfg.Rack.Bus.RootCA,
	}); errCon != nil {
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

	// 2. Initialize ID Generator
	// 2. Initialize ID Generator (Already done above)
	// mID := cfg.Rack.MachineID <-- Already defined
	// gen, err := idgen.New(mID) <-- Already done
	// if err != nil { return err }
	_ = gen // unused for now except for generating trace IDs if we wanted

	// 2.5 Initialize Runtime Manager (Rack Engine)
	opTimeout, _ := time.ParseDuration(cfg.Rack.Bus.OperationTimeout)

	// INSTRUMENTATION: Wrap Bus
	// We always wrap it so it can dynamically pick up metrics via GetMetrics()
	// even if telemetry initialization is deferred.
	managedBus := telemetry.NewInstrumentedBus(natsBus, nil)

	rtManager := rt.NewManager(logger, managedBus, gen, uint64(mID), clientName, opTimeout)
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
		if errSend := sendHello(managedBus, hello, gen); errSend != nil {
			logger.Error("Failed to send Hello", "error", errSend)
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

		sub, errSub := managedBus.Subscribe(enrollTopic, func(msg *fluxmsg.FluxMsg) {
			// Parse HelloResponse
			resp, errParse := fluxmsg.ParseHelloResponse(msg.Data)
			if errParse != nil {
				logger.Error("Failed to parse enrollment response", "error", errParse)
				return
			}
			logger.Info(resp.Message, "status", resp.Status)

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
				if errUnmarshal := msgpack.Unmarshal(resp.Passport, &env); errUnmarshal != nil {
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

				if os.Getenv("FLUXRIG_DISABLE_TELEMETRY") != "true" {
					shutdownTel, errInit := telemetry.Init(context.Background(), telCfg, natsBus, logBuffer, newGen)
					if errInit != nil {
						logger.Warn("Failed to init telemetry", "error", errInit)
					} else {
						defer func() {
							shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							if errStop := shutdownTel(shutdownCtx); errStop != nil {
								logger.Error("Telemetry shutdown error", "error", errStop)
							}
						}()
						// Update logger to use OTel Bridge
						logger = slog.Default().With(
							"component", string(loggerPkg.TypeRack),
							"name", state.Name,
						)
						slog.SetDefault(logger)
						logger.Info("Telemetry Initialized", "id", state.MachineID, "name", state.Name)
					}
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
				// Fallback: Init telemetry with best-effort identity (rack name from config)
				fallbackName := cfg.Rack.Name
				if fallbackName == "" {
					fallbackName = "pending"
				}
				entityID := gen.NewEntityID(idgen.EntityRack, 0)

				telCfg := telemetry.Config{
					ServiceName:         cfg.Telemetry.ServiceName,
					ServiceVersion:      version.Version,
					EntityID:            entityID,
					EntityName:          fallbackName,
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
					shutdownTel, errInit := telemetry.Init(context.Background(), telCfg, natsBus, logBuffer, gen)
					if errInit != nil {
						logger.Warn("Failed to init telemetry (fallback)", "error", errInit)
					} else {
						defer func() {
							shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							if errStop := shutdownTel(shutdownCtx); errStop != nil {
								logger.Error("Telemetry shutdown error", "error", errStop)
							}
						}()
						logger = slog.Default().With("component", string(loggerPkg.TypeRack), "name", fallbackName)
						slog.SetDefault(logger)
						logger.Info("Telemetry Initialized (fallback)", "name", fallbackName)
					}
				}
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
					shutdownTel, errInit := telemetry.Init(context.Background(), telCfg, natsBus, logBuffer, gen)
					if errInit != nil {
						logger.Warn("Failed to init telemetry (resume)", "error", errInit)
					} else {
						defer func() {
							shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							if errStop := shutdownTel(shutdownCtx); errStop != nil {
								logger.Error("Telemetry shutdown error", "error", errStop)
							}
						}()
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
			_, err = managedBus.Subscribe(notifyTopic, func(msg *fluxmsg.FluxMsg) {
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
				}
				// Handle Passport Update (Adoption)
				if len(hbResp.Passport) > 0 {
					env := pki.StateEnvelope{}
					if errAdopt := msgpack.Unmarshal(hbResp.Passport, &env); errAdopt != nil {
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

								// NOTE: Telemetry identity was set at init and doesn't need updating.
								// The entity_id and entity_name are fixed for the lifetime of this process.
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
		_, err = managedBus.Subscribe(scenarioTopic, func(msg *fluxmsg.FluxMsg) {
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

			if errApply := rtManager.ApplyScenario(context.Background(), &sc); errApply != nil {
				logger.Error("Failed to apply scenario", "error", errApply)
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
		if errHBInit := sendHeartbeat(ctx, managedBus, mID, cfg, gen); errHBInit != nil {
			logger.Warn("Failed to send initial heartbeat", "error", errHBInit)
		}
	}

	logger.With("component", "RACK", "name", cfg.Rack.Name).Info("Agent Running", "heartbeat", hbInterval)

	ticker := time.NewTicker(hbInterval)
	defer ticker.Stop()

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

	return b.Publish(fluxmsg.SubjectAgentHello, msg)
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
