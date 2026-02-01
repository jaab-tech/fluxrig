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

package mixer

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

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	fluxapi "github.com/jaab-tech/fluxrig/pkg/mixer/api"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	routerPkg "github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
)

// App encapsulates the FluxRig Mixer control plane application.
type App struct {
	cfg          *config.MixerConfig
	scenarioPath string
}

// NewApp creates a new Mixer application instance.
func NewApp(cfg *config.MixerConfig, scenarioPath string) *App {
	return &App{
		cfg:          cfg,
		scenarioPath: scenarioPath,
	}
}

// Run starts the Mixer and blocks until a signal is received.
func (a *App) Run() error {
	cfg := a.cfg

	// 1. Setup Data Directory
	if err := os.MkdirAll(cfg.Store.Dir, 0750); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}

	// 2. Load Security State
	clusterKeyPath := filepath.Join(cfg.Store.Dir, cfg.Store.ClusterKeyFile)
	slog.Info("Loading Cluster Authority", "path", clusterKeyPath)
	ck, err := pki.LoadClusterKey(clusterKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load cluster key (run 'fluxrig keys gen-cluster'): %w", err)
	}
	slog.Info("Cluster Authority Loaded", "public_key", hex.EncodeToString(ck.Public))

	slog.Info("Starting FluxRig Mixer",
		"version", version.String(),
		"api_port", cfg.API.Port,
		"snake_port", cfg.Snake.Port,
	)

	// 3. Initialize Store (DuckDB)
	storeDir := cfg.Store.Dir
	dbPath := filepath.Join(storeDir, cfg.Store.DatabaseFile)
	storeLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeMixer, "store")

	store, err := duckdb.NewStore(storeLogger, dbPath)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	if errMig := store.Migrate(context.Background()); errMig != nil {
		return fmt.Errorf("failed to run migrations: %w", errMig)
	}

	// 4. Initialize Registry
	reg := registry.NewDuckDBRegistry(store)

	// 4b. Self-Registration (Mixer Metadata)
	if cfg.Mixer.MixerName == "" {
		cfg.Mixer.MixerName = fmt.Sprintf("mixer-%02d-%s", cfg.Mixer.MachineID, idgen.RandomSuffix(4))
	}
	mixerName := cfg.Mixer.MixerName

	// Calculate ID (Type 0x02)
	idGen, _ := idgen.New(cfg.Mixer.MachineID)

	// Resume EntityID sequence from DB
	a.resumeEntitySequence(store, cfg.Mixer.MachineID, idGen)

	mixerEntityID := idGen.NewEntityID(idgen.EntityMixer, 1)
	apiAddr := fmt.Sprintf("localhost:%d", cfg.API.Port)

	// Register Mixer
	if errReg := store.RegisterMixer(context.Background(), cfg.Mixer.MachineID, mixerName, mixerEntityID, apiAddr, version.Version); errReg != nil {
		slog.Warn("Failed to register mixer", "error", errReg)
	} else {
		slog.Info("Mixer Registered", "entity_id", mixerEntityID, "name", mixerName)
	}

	// 5. Start Embedded NATS (Snake)
	snakeSrv, err := snake.NewServer(snake.Config{
		Port:           cfg.Snake.Port,
		ClusterName:    cfg.Snake.ClusterName,
		StoreDir:       filepath.Join(storeDir, "nats"),
		StreamName:     cfg.Snake.StreamName,
		StreamSubjects: cfg.Snake.StreamSubjects,
		TLSCert:        cfg.Snake.TLSCertFile,
		TLSKey:         cfg.Snake.TLSKeyFile,
	})
	if err != nil {
		return fmt.Errorf("failed to start embedded nats: %w", err)
	}
	defer snakeSrv.Shutdown()

	slog.Info("Snake (NATS) is ready", "url", snakeSrv.ClientURL())

	// Clear old snake sessions
	if errClr := store.ClearSnakes(context.Background()); errClr != nil {
		slog.Warn("Failed to clear old snakes", "error", errClr)
	}

	// Start Snake Stats Loop (Background)
	a.startSnakeStats(snakeSrv, store, idGen, mixerName, mixerEntityID, cfg)

	// 6. Start Router & Controllers
	wmLogger := loggerPkg.NewWatermillAdapter(slog.Default())
	router, err := routerPkg.NewRouter(wmLogger)
	if err != nil {
		return fmt.Errorf("failed to create router: %w", err)
	}

	jsUrl := cfg.Snake.URL
	if jsUrl == "" {
		jsUrl = "nats://localhost:4222"
	}

	if errJS := router.ConfigureJetStream(jsUrl, cfg.Snake.Durable, cfg.Snake.RootCAFile, wmLogger); errJS != nil {
		return fmt.Errorf("failed to configure router jetstream: %w", errJS)
	}

	// 7. Initialize Telemetry & Sink
	// 11. Metrics Cache (In-Memory) - Moved up for Sink wiring
	metricsCache := telemetry.NewMetricsCache()
	// NOTE: In Phase 2b we will wire the Ingest Sink to populate this cache. (Now Done)

	// Re-enable helper address for local connection
	cURL := snakeSrv.ClientURL()
	cURL = strings.Replace(cURL, "0.0.0.0", "127.0.0.1", 1)

	shutdownTel, stopSink := a.initTelemetry(cURL, mixerEntityID, mixerName, idGen, store, router, storeDir, cfg, metricsCache)
	defer func() {
		if shutdownTel != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if errShutdown := shutdownTel(shutdownCtx); errShutdown != nil {
				slog.Warn("Telemetry shutdown error", "error", errShutdown)
			}
		}
		if stopSink != nil {
			if errStop := stopSink(); errStop != nil {
				slog.Warn("Sink stop error", "error", errStop)
			}
		}
	}()

	// INSTRUMENTATION: Router Middleware
	// Must be done AFTER initTelemetry so GetMetrics() returns the initialized provider.
	if m := telemetry.GetMetrics(); m != nil {
		mw := telemetry.NewWatermillMiddleware(m)
		router.Router.AddMiddleware(mw.Middleware)
		slog.Info("Watermill Middleware Enabled for Telemetry")
	}

	// 8. Controllers
	pushDelay, _ := time.ParseDuration(cfg.Enrollment.PushDelay)
	enrollLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeScenario, "enrollment-ctrl")
	enrollment := controller.NewEnrollmentController(enrollLogger, reg, router.Pub, ck, mixerEntityID, pushDelay)
	enrollment.RegisterRoutes(router)

	// Run Router in background
	go func() {
		if errRouter := router.Router.Run(context.Background()); errRouter != nil {
			slog.Error("router failed", "error", errRouter)
			os.Exit(1)
		}
	}()

	// 9. Start API Server
	scenarioLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeScenario, "scenario-ctrl")
	sc := controller.NewScenarioController(scenarioLogger, "./data", store, idGen, mixerEntityID)

	// Create base bus
	baseBus := bus.NewNatsBus("flux-msg")
	// Wrap with instrumentation if metrics are available
	var scenarioBus bus.Bus = baseBus
	if m := telemetry.GetMetrics(); m != nil {
		scenarioBus = telemetry.NewInstrumentedBus(baseBus, m)
	}

	err = scenarioBus.Connect(cURL, bus.ConnectOptions{
		Name:           "mixer-scenario",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  1 * time.Second,
		RootCA:         cfg.Snake.RootCAFile,
	})
	if err != nil {
		slog.Warn("Failed to connect scenario bus", "error", err)
	} else {
		defer scenarioBus.Close()
		sc.SetBus(scenarioBus)
		enrollment.SetScenario(sc)
	}
	// 10. Bootstrap Scenario (if configured)
	if a.scenarioPath != "" {
		slog.Info("Bootstrapping Scenario", "path", a.scenarioPath)
		content, errRead := os.ReadFile(a.scenarioPath)
		if errRead != nil {
			slog.Error("Failed to read bootstrap scenario", "path", a.scenarioPath, "error", errRead)
			// Decide: Should we exit? Probably yes, as this was explicit intent.
			return fmt.Errorf("bootstrap scenario read failed: %w", errRead)
		}

		// Import (Validate & Persist)
		safeName, errImport := sc.Import(context.Background(), content, false)
		if errImport != nil {
			slog.Error("Failed to import bootstrap scenario", "error", errImport)
			return fmt.Errorf("bootstrap scenario import failed: %w", errImport)
		}

		// Activate
		if errActivate := sc.Activate(context.Background(), safeName); errActivate != nil {
			slog.Error("Failed to activate bootstrap scenario", "name", safeName, "error", errActivate)
			return fmt.Errorf("bootstrap scenario activation failed: %w", errActivate)
		}
		slog.Info("Bootstrap Scenario Activated", "name", safeName)
	}

	// 11. Start Janitor (Retention)
	janitorLogger := loggerPkg.WithComponent(slog.Default(), loggerPkg.TypeMixer, "janitor")
	janitor := telemetry.NewJanitor(janitorLogger, filepath.Join(storeDir, "telemetry"), cfg.Observability.Embedded.RetentionDays)
	janitorContext, cancelJanitor := context.WithCancel(context.Background())
	defer cancelJanitor()
	janitor.Start(janitorContext)

	// 11. Metrics Cache (In-Memory) - Initialized earlier
	// metricsCache := telemetry.NewMetricsCache()

	srv := fluxapi.NewServer(reg, router.Pub, ck, sc, metricsCache, mixerEntityID, cfg)
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
	sig := <-stop
	slog.Info("Received signal (ignored)", "signal", sig)

	sig2 := <-stop
	slog.Info("Shutting down Mixer...", "signal", sig2)
	return nil
}

func (a *App) resumeEntitySequence(store *duckdb.Store, machineID uint16, idGen *idgen.IDGenerator) {
	var maxSeq uint64
	var count int
	if err := store.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM registry").Scan(&count); err != nil {
		slog.Warn("Failed to count registry", "error", err)
	}

	row := store.DB().QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(entity_id & 1099511627775), 0) FROM registry WHERE (entity_id >> 40) & 65535 = ?",
		machineID)
	if err := row.Scan(&maxSeq); err != nil {
		slog.Warn("Failed to query max sequence", "error", err)
	}
	slog.Info("EntityID Resumption", "found_count", count, "resumed_seq", maxSeq, "machine_id", machineID)
	if maxSeq > 0 {
		idGen.SetSequence(maxSeq)
	}
}

func (a *App) initTelemetry(url string, eid uint64, name string, idGen *idgen.IDGenerator, store *duckdb.Store, router *routerPkg.RouterWrapper, storeDir string, cfg *config.MixerConfig, cache *telemetry.MetricsCache) (func(context.Context) error, func() error) {
	telBus := bus.NewNatsBus("flux-telemetry") // Explicit Telemetry Stream

	err := telBus.Connect(url, bus.ConnectOptions{
		Name:           "mixer-telemetry",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  1 * time.Second,
		RootCA:         cfg.Snake.RootCAFile,
	})
	if err != nil {
		slog.Warn("Failed to connect telemetry bus", "error", err)
		return nil, nil
	}

	// We must close telBus manually if we don't return a cleanup, but here we return cleanup.
	// Actually, Initialize returns shutdownTelemetry which is good.

	bufHandler := telemetry.NewBufferHandler(slog.Default().Handler())
	// NOTE: slog.Default should be the buffer logger from main, but here we are re-wrapping.
	// ideally we pass bufHandler from main, but for now let's assume global slog is set up.

	telCfg := telemetry.Config{
		ServiceName:         cfg.Telemetry.ServiceName,
		ServiceVersion:      version.Version,
		EntityID:            eid,
		EntityName:          name,
		BatchIntervalString: cfg.Telemetry.BatchInterval,
		MaxBatchSize:        cfg.Telemetry.MaxBatchSize,
		BaseSubject:         cfg.Telemetry.BaseSubject,
		Component:           string(loggerPkg.TypeMixer),
		Logging:             cfg.Logging,
		Store:               cfg.Store,
		StdoutEnabled:       true,
		StdoutLevel:         "info",
		Metrics: telemetry.MetricsConfig{
			HostEnabled:    cfg.Telemetry.Metrics.HostEnabled,
			RuntimeEnabled: cfg.Telemetry.Metrics.RuntimeEnabled,
			BentoEnabled:   cfg.Telemetry.Metrics.BentoEnabled,
		},
	}

	shutdownTelemetry, err := telemetry.Init(context.Background(), telCfg, telBus, bufHandler, idGen)
	if err != nil {
		slog.Warn("Failed to initialize telemetry", "error", err)
		telBus.Close()
		return nil, nil
	}

	// Update Global Logger with Attributes
	l := slog.Default().With(
		"component", string(loggerPkg.TypeMixer),
		"name", name,
	)
	slog.SetDefault(l)
	slog.Info("Telemetry Initialized")

	// Start Telemetry Sink
	flushInterval, _ := time.ParseDuration(cfg.Ingest.FlushInterval)
	sink := ingest.NewTelemetrySink(telBus, store, filepath.Join(storeDir, "telemetry"), flushInterval, cache)

	stopSink := func() error {
		err := sink.Stop()
		telBus.Close()
		return err
	}

	if err := sink.Start(); err != nil {
		slog.Error("Failed to start telemetry sink", "error", err)
		_ = stopSink() // cleanup
		return shutdownTelemetry, nil
	}

	slog.Info("Telemetry Sink Started", "flush_interval", flushInterval.String())
	return shutdownTelemetry, stopSink
}

func (a *App) startSnakeStats(snakeSrv *snake.Server, store *duckdb.Store, idGen *idgen.IDGenerator, mixerName string, mixerEntityID uint64, cfg *config.MixerConfig) {
	// We map NATS Connection ID (CID) -> Snake EntityID
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

			// 1. Process Active Connections
			for _, c := range clients {
				currentEID, known := snakeSessions[c.CID]

				if !known {
					if c.Name == mixerName {
						continue
					}

					rackID, err := store.GetEntityIDByName(context.Background(), c.Name)
					if err != nil {
						continue
					}

					snakeName := fmt.Sprintf("snake-%s", c.Name)
					eid, err := store.GetEntityIDByName(context.Background(), snakeName)
					if err != nil {
						eid = idGen.NextEntityID(idgen.EntitySnake)
					}

					mixerIP := "127.0.0.1"
					mixerPort := cfg.Snake.Port
					if u, err := url.Parse(snakeSrv.ClientURL()); err == nil {
						h, pStr, err := net.SplitHostPort(u.Host)
						if err == nil && h != "" && h != "0.0.0.0" {
							mixerIP = h
						}
						if p, err := strconv.Atoi(pStr); err == nil {
							mixerPort = p
						}
					}

					if err := store.RegisterSnake(context.Background(), snakeName, eid, version.Version, rackID, uint64(mixerEntityID), c.IP, c.Port, mixerIP, mixerPort, cfg.Mixer.MachineID); err != nil {
						slog.Warn("Failed to register snake", "name", snakeName, "error", err)
						continue
					}
					snakeSessions[c.CID] = eid
					currentEID = eid
				}

				stats := map[string]any{
					"in_msgs":   c.InMsgs,
					"out_msgs":  c.OutMsgs,
					"in_bytes":  c.InBytes,
					"out_bytes": c.OutBytes,
					"uptime":    c.Uptime,
				}
				if err := store.UpdateSnakeStats(context.Background(), currentEID, stats); err != nil {
					slog.Debug("Failed to update snake stats", "eid", currentEID, "error", err)
				}
			}

			// 2. Prune Dead Sessions
			eidRefs := make(map[uint64]int)
			for cid, eid := range snakeSessions {
				if _, active := clientMap[cid]; active {
					eidRefs[eid]++
				}
			}

			for cid, eid := range snakeSessions {
				if _, active := clientMap[cid]; !active {
					if eidRefs[eid] == 0 {
						slog.Info("Pruning Dead Snake Entity", "eid", eid)
						if err := store.RemoveSnake(context.Background(), eid); err != nil {
							slog.Warn("Failed to remove dead snake", "eid", eid, "error", err)
						}
					}
					delete(snakeSessions, cid)
				}
			}
		}
	}()
}
