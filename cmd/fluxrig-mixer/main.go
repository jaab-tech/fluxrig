package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	// DEBUG: Print loaded config
	fmt.Printf("DEBUG CONFIG: %+v\n", cfg)

	// 3. Setup Standard Logger
	logger := loggerPkg.New(loggerPkg.Config{
		Level:      cfg.Logging.Level, // Config must have this, checking...
		EntityType: loggerPkg.TypeMixer,
		Name:       "fluxrig-mixer",
		Writer:     os.Stdout,
	})
	// Buffer pre-telemetry logs for 1-to-1 parity
	bufHandler := telemetry.NewBufferHandler(logger.Handler())
	bufLogger := slog.New(bufHandler)
	slog.SetDefault(bufLogger)

	// 4. Load Security State (Cluster Key)
	// Key Path relative to Store Dir
	if err := os.MkdirAll(cfg.Store.Dir, 0755); err != nil {
		slog.Error("failed to create store directory", "dir", cfg.Store.Dir, "error", err)
		os.Exit(1)
	}
	clusterKeyPath := filepath.Join(cfg.Store.Dir, cfg.Store.ClusterKeyFile)

	slog.Info("Loading Cluster Authority", "path", clusterKeyPath)
	ck, err := pki.LoadClusterKey(clusterKeyPath)
	if err != nil {
		slog.Error("failed to load cluster key. Run 'fluxrig keys gen-cluster -o <path>' first", "path", clusterKeyPath, "error", err)
		os.Exit(1)
	}
	slog.Info("Cluster Authority Loaded", "public_key", hex.EncodeToString(ck.Public))

	slog.Info("Starting FluxRig Mixer",
		"version", version.String(),
		"api_port", cfg.API.Port,
		"snake_port", cfg.Snake.Port,
	)

	// 2a. Data Directory Initialization
	// Already ensured above (cfg.Store.Dir)
	storeDir := cfg.Store.Dir

	// 3. Initialize Store (DuckDB)
	dbPath := filepath.Join(storeDir, cfg.Store.DatabaseFile)
	store, err := duckdb.NewStore(dbPath)
	if err != nil {
		slog.Error("failed to open store", "path", dbPath, "error", err)
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
	if cfg.Mixer.MixerName == "" {
		cfg.Mixer.MixerName = fmt.Sprintf("mixer-%02d-%s", cfg.Mixer.MachineID, idgen.RandomSuffix(4))
	}
	mixerName := cfg.Mixer.MixerName

	// Calculate ID (Type 0x02)
	idGen, _ := idgen.New(cfg.Mixer.MachineID) // Handles EntityID construction

	// Resume EntityID sequence from DB to avoid collisions across restarts
	var maxSeq uint64
	var count int
	_ = store.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM registry").Scan(&count)

	// We look for the maximum local sequence (bottom 40 bits) where the embedded MachineID (bits 40-55) matches ours.
	row := store.DB().QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(entity_id & 1099511627775), 0) FROM registry WHERE (entity_id >> 40) & 65535 = ?",
		cfg.Mixer.MachineID)
	if err := row.Scan(&maxSeq); err != nil {
		slog.Warn("Failed to query max sequence", "error", err)
	}
	slog.Info("EntityID Resumption", "found_count", count, "resumed_seq", maxSeq, "machine_id", cfg.Mixer.MachineID)
	if maxSeq > 0 {
		idGen.SetSequence(maxSeq)
	}

	mixerEntityID := idGen.NewEntityID(idgen.EntityMixer, 1)
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
		StoreDir:       filepath.Join(storeDir, "nats"),
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

					// Stable Snake Identity: Name-Based Idempotence
					// Reuses existing ID if name matches, or takes next available sequence.
					snakeName := fmt.Sprintf("snake-%s", c.Name) // Format: snake-<RackName>
					eid, err := store.GetEntityIDByName(context.Background(), snakeName)
					if err != nil {
						eid = idGen.NextEntityID(idgen.EntitySnake)
					}

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
					// Use Snake Logger (if we had one in this scope, but for now we are in main. Let's use a local snake logger)
					snakeLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeSnake, snakeName)
					snakeLogger.Info("Snake Registered/Updated", "name", snakeName, "eid", eid, "from_rack", c.Name)

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
				// Update statistics (non-blocking)
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

	// 5c. Telemetry Init moved after Router setup to ensure Streams exist

	// 6. Start Router & Controllers
	// Create Watermill Router with Adapter
	wmLogger := loggerPkg.NewWatermillAdapter(slog.Default())
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
	if err := router.ConfigureJetStream(jsUrl, cfg.Snake.Durable, wmLogger); err != nil {
		slog.Error("failed to configure router jetstream", "error", err)
		os.Exit(1)
	}

	// 5c. Initialize Telemetry (Self-Monitoring)
	// Moved here so Streams exist
	telBus := bus.NewNatsBus("flux-telemetry") // Explicit Telemetry Stream
	// Use local connection
	// Fix: Replace 0.0.0.0 with 127.0.0.1 for local dialing (NEX-4223)
	cURL := snakeSrv.ClientURL()
	cURL = strings.Replace(cURL, "0.0.0.0", "127.0.0.1", 1)

	if err := telBus.Connect(cURL, mixerName, 5*time.Second, 1*time.Second); err != nil {
		slog.Warn("Failed to connect telemetry bus", "error", err)
	} else {
		// Note: defer is scoped to main(), so this is fine
		defer telBus.Close()

		telCfg := telemetry.Config{
			ServiceName:         "flux-mixer",
			ServiceVersion:      version.Version,
			EntityID:            mixerEntityID,
			EntityName:          mixerName,
			BatchIntervalString: "5s",
			BaseSubject:         "flux.telemetry",
			Component:           string(loggerPkg.TypeMixer),
			Logging:             cfg.Logging,
			Store:               cfg.Store,
		}

		shutdownTelemetry, err := telemetry.Init(context.Background(), telCfg, telBus, bufHandler, idGen)
		if err != nil {
			slog.Warn("Failed to initialize telemetry", "error", err)
		} else {
			defer func() {
				if err := shutdownTelemetry(context.Background()); err != nil {
					slog.Error("Telemetry shutdown error", "error", err)
				}
			}()
			// Update Global Logger with Attributes
			l := slog.Default().With(
				"component", string(loggerPkg.TypeMixer),
				"name", mixerName,
			)
			slog.SetDefault(l)
			slog.Info("Telemetry Initialized")
		}

		// Start Telemetry Sink (Ingestion)
		sink := ingest.NewTelemetrySink(telBus, store, filepath.Join(storeDir, "telemetry"))
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

	// Controllers
	pushDelay, _ := time.ParseDuration(cfg.Enrollment.PushDelay)
	enrollLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeScenario, "enrollment-ctrl")
	enrollment := controller.NewEnrollmentController(enrollLogger, reg, router.Pub, ck, mixerEntityID, pushDelay)
	enrollment.RegisterRoutes(router)

	// Run Router in background
	go func() {
		if err := router.Router.Run(context.Background()); err != nil {
			slog.Error("router failed", "error", err)
			os.Exit(1)
		}
	}()

	// 7. Start API Server
	// Scenario Controller (Phase 3: Use local PWD as git repo)
	scenarioLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeScenario, "scenario-ctrl")
	sc := controller.NewScenarioController(scenarioLogger, "./data", store, idGen, mixerEntityID)

	// Create dedicated bus for scenario distribution to racks
	scenarioBus := bus.NewNatsBus("flux-msg") // Explicit Business Stream
	if err := scenarioBus.Connect(snakeSrv.ClientURL(), "mixer-scenario", 5*time.Second, 1*time.Second); err != nil {
		slog.Warn("Failed to connect scenario bus", "error", err)
	} else {
		defer scenarioBus.Close()
		sc.SetBus(scenarioBus)     // Enable scenario push to racks via NATS
		enrollment.SetScenario(sc) // Wire Enrollment to Scenario for auto-push on registration
	}

	srv := api.NewServer(reg, router.Pub, ck, sc, mixerEntityID, cfg)
	go func() {
		if err := srv.Start(fmt.Sprintf(":%d", cfg.API.Port)); err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	slog.Info("Mixer is ready")

	// Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	slog.Info("Shutting down Mixer...")
	// Defers will run now (Telemetry Sink, Store Close, Snake Shutdown)
}
