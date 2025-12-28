package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jaab-tech/fluxrig/cmd/fluxrig-mixer/api"
	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	routerPkg "github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
)

func main() {
	// 1. Parse Flags
	var configPath string
	if len(os.Args) > 2 && os.Args[1] == "-c" {
		configPath = os.Args[2]
	} else if os.Getenv("FLUXRIG_CONFIG") != "" {
		configPath = os.Getenv("FLUXRIG_CONFIG")
	} else {
		configPath = "fluxrig-mixer.toml"
	}

	// 2. Load Configuration (to get Log Level)
	cfg, err := config.LoadMixer(configPath)
	if err != nil {
		// Fallback logger if config load fails
		l := loggerPkg.New(loggerPkg.Config{
			Level:     "info",
			Component: loggerPkg.TypeMixer,
			Name:      "fluxrig-mixer-init",
			Writer:    os.Stdout,
		})
		l.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// 3. Setup Standard Logger
	logger := loggerPkg.New(loggerPkg.Config{
		Level:     cfg.Logging.Level, // Config must have this, checking...
		Component: loggerPkg.TypeMixer,
		Name:      "fluxrig-mixer",
		Writer:    os.Stdout,
	})
	slog.SetDefault(logger)

	// 4. Load Security State (Cluster Key)
	slog.Info("Loading Cluster Authority", "path", cfg.Store.ClusterKeyPath)
	ck, err := pki.LoadClusterKey(cfg.Store.ClusterKeyPath)
	if err != nil {
		slog.Error("failed to load cluster key. Run 'fluxrig keys gen-cluster -o <path>' first", "path", cfg.Store.ClusterKeyPath, "error", err)
		os.Exit(1)
	}
	slog.Info("Cluster Authority Loaded", "public_key", hex.EncodeToString(ck.Public))

	slog.Info("Starting FluxRig Mixer",
		"version", version.String(),
		"api_port", cfg.API.Port,
		"snake_port", cfg.Snake.Port,
	)

	// 2a. Data Folder Hygiene: Auto-create data directory configuration path
	// We extract the directory from the store path and ensure it exists.
	// Simple assumption: Store path is like "data/fluxrig.duckdb"
	storeDir := "data"
	if idx := strings.LastIndex(cfg.Store.Path, "/"); idx != -1 {
		storeDir = cfg.Store.Path[:idx]
	}
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		slog.Error("failed to create data directory", "dir", storeDir, "error", err)
		os.Exit(1)
	}

	// 3. Initialize Store (DuckDB)
	store, err := duckdb.NewStore(cfg.Store.Path)
	if err != nil {
		slog.Error("failed to open store", "path", cfg.Store.Path, "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.InitializeSchema(context.Background()); err != nil {
		slog.Error("failed to initialize schema", "error", err)
		os.Exit(1)
	}
	if err := store.InitializeTelemetrySchema(context.Background()); err != nil {
		slog.Error("failed to initialize telemetry schema", "error", err)
		os.Exit(1)
	}

	// 4. Initialize Registry
	reg := registry.NewDuckDBRegistry(store)

	// 4b. Self-Registration (Mixer Metadata)
	mixerName := fmt.Sprintf("mixer-%02d", cfg.Mixer.MachineID)
	// Calculate ID (Type 0x02)
	idGen, _ := idgen.New(cfg.Mixer.MachineID) // Handles EntityID construction
	mixerEntityID := idGen.NewEntityID(idgen.EntityMixer, 0)
	apiAddr := fmt.Sprintf("localhost:%d", cfg.API.Port)

	// Register Mixer
	if err := store.RegisterMixer(context.Background(), cfg.Mixer.MachineID, mixerName, mixerEntityID, apiAddr, version.Version); err != nil {
		slog.Warn("Failed to register mixer", "error", err)
	} else {
		slog.Info("Mixer Registered", "entity_id", mixerEntityID, "name", mixerName)
	}

	// 5. Start Embedded NATS (Snake)
	snakeSrv, err := snake.NewServer(snake.Config{
		Port:           cfg.Snake.Port,
		ClusterName:    cfg.Snake.ClusterName,
		StoreDir:       cfg.Snake.StoreDir,
		StreamName:     cfg.Snake.StreamName,
		StreamSubjects: cfg.Snake.StartSubjects,
	})
	if err != nil {
		slog.Error("failed to start embedded nats", "error", err)
		os.Exit(1)
	}
	defer snakeSrv.Shutdown()

	slog.Info("Snake (NATS) is ready", "url", snakeSrv.ClientURL())

	// Clear old snake sessions from DB
	if err := store.ClearSnakes(context.Background()); err != nil {
		slog.Warn("Failed to clear old snakes", "error", err)
	}

	// 5b. Start Snake Stats Loop (Dynamic Discovery)
	// We map NATS Connection ID (CID) -> Snake EntityID
	// This allows us to track individual connections as "Snakes"
	snakeSessions := make(map[uint64]uint64)

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			clients, err := snakeSrv.Clients()
			if err != nil {
				slog.Debug("Failed to get snake clients", "error", err)
				continue
			}

			// Map CIDs to Clients for fast lookup
			clientMap := make(map[uint64]snake.ClientInfo)
			for _, c := range clients {
				clientMap[c.CID] = c
			}

			// 1. Process Active Connections (Register/Update)
			for _, c := range clients {
				// We only care about Racks (or known entities)
				// Filter by name or checking registry

				// Check known session
				currentEID, known := snakeSessions[c.CID]

				// New Connection Logic (or if we need to re-verify identity)
				if !known {
					// Skip Mixer Self-Connection (Not a Snake)
					if c.Name == mixerName {
						continue
					}

					// New Connection! Resolve Rack.
					rackID, err := store.GetEntityIDByName(context.Background(), c.Name)
					if err != nil {
						// Unknown entity
						continue
					}

					// Stable Snake Identity: Derived from Rack MachineID
					// Extract MachineID from Rack EntityID (bits 40-55)
					// Logic matches pkg/idgen/idgen.go
					//nolint:gosec // Safe extraction of MachineID
					rackMachineID := uint16((rackID >> 40) & 0xFFFF)

					// ID: [Type Snake] [MixerID] [RackMachineID (Sequence)]
					eid := idGen.NewEntityID(idgen.EntitySnake, uint64(rackMachineID))
					snakeName := fmt.Sprintf("snake-%s", c.Name) // Format: snake-<RackName>

					// Parse Mixer Address
					mixerURLStr := snakeSrv.ClientURL()
					mixerIP := "127.0.0.1"
					mixerPort := cfg.Snake.Port
					if u, err := url.Parse(mixerURLStr); err == nil {
						host, portStr, err := net.SplitHostPort(u.Host)
						if err == nil && host != "" && host != "0.0.0.0" && host != "[::]" {
							mixerIP = host
						}
						if p, err := strconv.Atoi(portStr); err == nil {
							mixerPort = p
						}
					}

					// Register (Upsert)
					if err := store.RegisterSnake(context.Background(), snakeName, eid, version.Version, rackID, uint64(mixerEntityID), c.IP, c.Port, mixerIP, mixerPort, cfg.Mixer.MachineID); err != nil {
						slog.Warn("Failed to register snake", "name", snakeName, "error", err)
						continue
					}
					slog.Info("Snake Registered/Updated", "name", snakeName, "eid", eid, "from_rack", c.Name)

					snakeSessions[c.CID] = eid
					currentEID = eid
				}

				// Update Stats (for all active sessions)
				stats := map[string]any{
					"in_msgs":   c.InMsgs,
					"out_msgs":  c.OutMsgs,
					"in_bytes":  c.InBytes,
					"out_bytes": c.OutBytes,
					"uptime":    c.Uptime,
				}
				// Best effort update
				_ = store.UpdateSnakeStats(context.Background(), currentEID, stats)
			}

			// 2. Safe Pruning (Reference Counting)
			// Calculate active reference count for each EID
			eidRefs := make(map[uint64]int)
			for cid, eid := range snakeSessions {
				if _, active := clientMap[cid]; active {
					eidRefs[eid]++
				}
			}

			// Prune dead sessions
			for cid, eid := range snakeSessions {
				if _, active := clientMap[cid]; !active {
					// Client Disconnected
					// Only remove from DB if NO OTHER active connection references this EID
					if eidRefs[eid] == 0 {
						slog.Info("Pruning Dead Snake Entity", "eid", eid)
						if err := store.RemoveSnake(context.Background(), eid); err != nil {
							slog.Warn("Failed to remove dead snake", "eid", eid, "error", err)
						}
					} else {
						slog.Info("Snake session closed but entity active (reconnected)", "cid", cid, "eid", eid)
					}
					// Remove session from map
					delete(snakeSessions, cid)
				}
			}
		}
	}()

	// 5c. Initialize Telemetry (Self-Monitoring)
	telBus := bus.NewNatsBus(cfg.Snake.StreamName)
	// Use local connection
	if err := telBus.Connect(snakeSrv.ClientURL(), mixerName, 5*time.Second, 1*time.Second); err != nil {
		slog.Warn("Failed to connect telemetry bus", "error", err)
	} else {
		defer telBus.Close()

		// Telemetry
		// Mixer EntityID (mixerEntityID) was calculated above.

		telCfg := telemetry.Config{
			ServiceName:         "flux-mixer",
			ServiceVersion:      version.Version,
			EntityID:            mixerEntityID,
			EntityName:          mixerName,
			BatchIntervalString: "5s",
			BaseSubject:         "flux.telemetry",
		}

		shutdownTelemetry, err := telemetry.Init(context.Background(), telCfg, telBus)
		if err != nil {
			slog.Warn("Failed to initialize telemetry", "error", err)
		} else {
			defer func() {
				if err := shutdownTelemetry(context.Background()); err != nil {
					slog.Error("Telemetry shutdown error", "error", err)
				}
			}()
			slog.Info("Telemetry Initialized")
		}

		// Start Telemetry Sink (Ingestion)
		// We use the same bus connection (telBus) which is connected to Snake
		sink := ingest.NewTelemetrySink(telBus, store, cfg.Observability.Embedded.DataDir)
		if err := sink.Start(); err != nil {
			slog.Error("Failed to start telemetry sink", "error", err)
		} else {
			defer func() {
				if err := sink.Stop(); err != nil {
					slog.Error("Failed to stop sink", "error", err)
				}
			}()
			slog.Info("Telemetry Sink Started")
		}
	}

	// 6. Start Router & Controllers
	// Create Watermill Router with Adapter
	wmLogger := loggerPkg.NewWatermillAdapter(logger)
	router, err := routerPkg.NewRouter(wmLogger)
	if err != nil {
		slog.Error("failed to create router", "error", err)
		os.Exit(1)
	}

	// Configure JetStream
	// Using "fluxrig" as the durable/embedded URL or snake URL
	jsUrl := cfg.Snake.URL
	if jsUrl == "" {
		jsUrl = "nats://localhost:4222"
	}

	slog.Debug("Configuring JetStream", "url", jsUrl)
	if err := router.ConfigureJetStream(jsUrl, cfg.Snake.StreamName, wmLogger); err != nil {
		slog.Error("failed to configure router jetstream", "error", err)
		os.Exit(1)
	}

	// Controllers
	enrollment := controller.NewEnrollmentController(reg, router.Pub, ck)
	enrollment.RegisterRoutes(router)

	// Run Router in background
	go func() {
		if err := router.Router.Run(context.Background()); err != nil {
			slog.Error("router failed", "error", err)
			os.Exit(1)
		}
	}()

	// 7. Start API Server
	srv := api.NewServer(reg, router.Pub, ck)
	go func() {
		if err := srv.Start(fmt.Sprintf(":%d", cfg.API.Port)); err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	slog.Info("Mixer is ready")
	select {} // Block forever
}
