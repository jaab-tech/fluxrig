package commands

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"fmt"
	"os"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/vmihailenco/msgpack/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// ErrReconnect indicates the agent needs to restart its session (e.g. identity change)
var ErrReconnect = fmt.Errorf("reconnect needed")

// RunAgent implements the main loop of the FluxRig Rack Agent.
func RunAgent(cfg *config.RackConfig, logger *slog.Logger) error {
	for {
		err := runSession(cfg, logger)
		if err == ErrReconnect {
			logger.Info("Restarting Session (Identity Changed)")
			// Brief pause to allow connection cleanup propagation
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return err
	}
}

func runSession(cfg *config.RackConfig, logger *slog.Logger) error {
	// 0. Try Load Passport (Offline Capability)
	statePath := cfg.Rack.DataDir + "/state.flux"
	logger.Info("Attempting to load Passport", "path", statePath)
	var secret string
	if env, err := pki.LoadStateEnvelope(statePath); err == nil {
		if s, err := env.Verify(); err == nil {
			logger.Info("Loaded Cached Passport", "id", s.MachineID, "name", s.Name)
			cfg.Rack.MachineID = s.MachineID
			cfg.Rack.Name = s.Name // Ensure name is loaded for telemetry init
			secret = s.Secret
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
		initialID := cfg.Rack.MachineID
		initialName := "pending"
		if cfg.Rack.MachineID > 0 && cfg.Rack.Name != "" {
			initialName = cfg.Rack.Name
		}

		gen, _ := idgen.New(initialID)
		entityID := gen.NewEntityID(idgen.EntityRack, 0)

		telCfg := telemetry.Config{
			ServiceName:         "flux-rack",
			ServiceVersion:      version.Version,
			EntityID:            entityID,
			EntityName:          initialName,
			BatchIntervalString: "5s",
			BaseSubject:         "flux.telemetry",
		}

		shutdownTel, err := telemetry.Init(context.Background(), telCfg, natsBus)
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
			// logs are captured by the OTel exporter.
			logger = slog.Default()
			logger.Info("Telemetry Initialized")
		}
	}

	// 2. Initialize ID Generator
	mID := cfg.Rack.MachineID
	gen, err := idgen.New(mID)
	if err != nil {
		return err
	}
	_ = gen // unused for now except for generating trace IDs if we wanted

	// 3. Send Hello (If Connected)
	// helloName determined earlier

	hello := &fluxmsg.HelloPayload{
		Name:      helloName,
		Secret:    secret,
		MachineID: mID,
		IP:        "127.0.0.1",
		Port:      8092,
		Version:   version.Version,
	}

	if busConnected {
		if err := sendHello(natsBus, hello); err != nil {
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

				// Save
				statePath := cfg.Rack.DataDir + "/state.flux"
				if err := os.MkdirAll(cfg.Rack.DataDir, 0755); err != nil {
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

			// Subscribe to Notifications (Status Updates)
			notifyTopic := fmt.Sprintf("fluxrig.agent.notify.%d", mID)
			notifSub, err := natsBus.Subscribe(notifyTopic, func(msg *fluxmsg.FluxMsg) {
				hbResp, err := fluxmsg.ParseHeartbeatResponse(msg.Data)
				if err != nil {
					return
				}
				if hbResp.Status != currentStatus {
					logger.Info("Status Changed", "old", currentStatus, "new", hbResp.Status)
					currentStatus = hbResp.Status
				}
				if hbResp.Command != "" {
					logger.Info("Received Command", "cmd", hbResp.Command)
				}

				// Handle Passport Update (Adoption)
				if len(hbResp.Passport) > 0 {
					logger.Info("Received Updated Passport")
					env := pki.StateEnvelope{Payload: hbResp.Passport}
					if err := msgpack.Unmarshal(hbResp.Passport, &env); err == nil {
						if newState, err := env.Verify(); err == nil {
							// Save it
							if err := env.Save(statePath); err == nil {
								logger.Info("Passport Updated and Saved", "new_name", newState.Name)

								// Signal Reconnect if name changed
								if newState.Name != helloName {
									logger.Info("Identity changed, signalling restart", "old", helloName, "new", newState.Name)
									select {
									case reconnectCh <- struct{}{}:
									default:
									}
								}
							} else {
								logger.Error("Failed to save updated passport", "error", err)
							}
						} else {
							logger.Error("Invalid updated passport signature", "error", err)
						}
					} else {
						logger.Error("Failed to unmarshal updated passport", "error", err)
					}
				}
			})
			if err != nil {
				logger.Error("Failed to subscribe to notify", "error", err)
			} else {
				logger.Info("Listening for notifications", "topic", notifyTopic)
				defer func() { _ = notifSub.Unsubscribe() }()
			}

		case <-time.After(enrollTimeout):
			if cfg.Rack.MachineID == 0 {
				logger.Warn("Enrollment Timeout. No Passport received.")
				// Fallback or retry? For now continue as pending.
			}
		}
	} else {
		logger.Info("Skipping Enrollment (Offline)")
	}

	// 4. Heartbeat Loop
	// Initial heartbeat
	if busConnected {
		if err := sendHeartbeat(natsBus, mID); err != nil {
			logger.Warn("Failed to send initial heartbeat", "error", err)
		}
	}

	logger.Info("Agent Running", "heartbeat", hbInterval)

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
		case <-reconnectCh:
			return ErrReconnect
		case <-ticker.C:
			if busConnected {
				// We use current mID (might have changed after passport load)
				if err := sendHeartbeat(natsBus, mID); err != nil {
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

func sendHello(b *bus.NatsBus, p *fluxmsg.HelloPayload) error {
	data, err := p.ToData()
	if err != nil {
		return err
	}

	msg := fluxmsg.New()
	msg.Data = data
	// Need to set src_id if we have it
	msg.SrcGearID = uint64(p.MachineID)

	return b.Publish(fluxmsg.SubjectAgentHello, msg)
}

func sendHeartbeat(b *bus.NatsBus, mid uint16) error {
	stats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
	}

	p := &fluxmsg.HeartbeatPayload{
		MachineID: mid,
		Stats:     stats,
	}

	data, err := p.ToData()
	if err != nil {
		return err
	}

	msg := fluxmsg.New()
	msg.Data = data
	msg.SrcGearID = uint64(mid)

	return b.Publish(fluxmsg.SubjectAgentHeartbeat, msg)
}
