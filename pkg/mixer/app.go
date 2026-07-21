// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/logger/rotator"
	"github.com/jaab-tech/fluxrig/pkg/mixer/api"
	"github.com/jaab-tech/fluxrig/pkg/netutil"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/router"
	"github.com/jaab-tech/fluxrig/pkg/snake"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

// App is the main application entry point for the Mixer.
type App struct {
	cfg           *config.MixerConfig
	scenarioRef   string
	log           *slog.Logger
	bufHandler    *telemetry.BufferHandler
	switchHandler *telemetry.SwitchableHandler
}

// NewApp creates a new Mixer application instance.
func NewApp(cfg *config.MixerConfig, scenarioRef string, bufHandler *telemetry.BufferHandler) *App {
	return &App{
		cfg:         cfg,
		scenarioRef: scenarioRef,
		log:         slog.Default().With("component", "MIXER"),
		bufHandler:  bufHandler,
	}
}

// Run initializes and starts the Mixer service.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Unified Logger Setup
	lLevelStr := a.cfg.Logging.Level
	if lLevelStr == "" {
		lLevelStr = "info"
	}

	var logWriter io.Writer = os.Stdout
	if a.cfg.Logging.Filename != "" {
		// Ensure log directory exists
		logDir := filepath.Dir(a.cfg.Logging.Filename)
		if logDir != "." && logDir != "/" {
			_ = os.MkdirAll(logDir, 0750)
		}

		rot, err := rotator.New(a.cfg.Logging.Filename, a.cfg.Logging.MaxSizeMB, a.cfg.Logging.MaxBackups, a.cfg.Logging.Compress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to init Mixer log rotator: %v\n", err)
		} else {
			logWriter = rot
		}
	}

	baseLogger := loggerPkg.New(loggerPkg.Config{
		Level:      lLevelStr,
		EntityType: loggerPkg.TypeMixer,
		Name:       a.cfg.Base.Name,
		Writer:     logWriter,
	})
	a.switchHandler = telemetry.NewSwitchableHandler(baseLogger.Handler())
	a.bufHandler = telemetry.NewBufferHandler(telemetry.NewSourceHandler(a.switchHandler))
	a.log = slog.New(a.bufHandler)

	// 2. Storage & Registry Initialization
	if a.cfg.Store.Dir != "" && a.cfg.Store.Dir != ":memory:" {
		if errDir := os.MkdirAll(a.cfg.Store.Dir, 0750); errDir != nil {
			return fmt.Errorf("failed to create store directory: %w", errDir)
		}
	}

	dbFile := a.cfg.Store.DatabaseFile
	if dbFile == "" {
		dbFile = "flux.duckdb"
	}
	dbPath := filepath.Join(a.cfg.Store.Dir, dbFile)
	if a.cfg.Store.Dir == "" {
		dbPath = ":memory:"
	}

	store, err := duckdb.NewStore(a.log, dbPath)
	if err != nil {
		return fmt.Errorf("failed to init store: %w", err)
	}
	defer func() { _ = store.Close() }()

	if errMig := store.Migrate(ctx); errMig != nil {
		return fmt.Errorf("failed to migrate store: %w", errMig)
	}
	store.SetAutoAdopt(a.cfg.Enrollment.AutoAdopt)

	// 2. Identity Management (Sovereign Passport)
	keyPath := filepath.Join(a.cfg.Store.Dir, a.cfg.Store.ClusterKeyFile)
	clusterKey, err := pki.LoadClusterKey(keyPath)
	if err != nil {
		a.log.Info("Cluster key not found. Generating new authority keypair...", "path", keyPath)
		var errGen error
		clusterKey, errGen = pki.GenerateClusterKey()
		if errGen != nil {
			return fmt.Errorf("failed to generate cluster key: %w", errGen)
		}
		if errSave := clusterKey.Save(keyPath); errSave != nil {
			return fmt.Errorf("failed to save cluster key: %w", errSave)
		}
	}

	statePath := filepath.Join(a.cfg.Store.Dir, "mixer.flux")
	mixerState, err := pki.LoadMixerState(statePath, clusterKey.Public)
	if err != nil {
		a.log.Info("Mixer identity not found. Generating sovereign passport...")
		mixerState = &pki.MixerState{
			MachineID:   uuid.New(),
			Name:        a.cfg.Base.Name,
			Version:     version.Version,
			CreatedAt:   time.Now().Unix(),
			UpdatedAt:   time.Now().Unix(),
			UpdateCount: 0,
		}
		env, _ := clusterKey.SignMixer(mixerState)
		if errEnv := env.Save(statePath); errEnv != nil {
			return fmt.Errorf("failed to save mixer identity: %w", errEnv)
		}
	}

	// ID Generator
	idGen, _ := idgen.New(mixerState.MachineID)
	mixerEntityID := idGen.NextEntityID(idgen.EntityMixer)

	a.bufHandler = telemetry.NewBufferHandler(telemetry.NewSourceHandler(a.switchHandler))
	a.log = slog.New(a.bufHandler)

	// 3b. Snake Initialization (Embedded NATS/JetStream)
	subjects := a.cfg.Snake.StreamSubjects
	if len(subjects) == 0 {
		subjects = []string{
			"flux.msg.>",
			"flux.agent.>",
			"flux.rack.>",
			"flux.ctrl.>",
		}
		// If domain is not 'flux', add domain-prefixed subjects too for backward compatibility/isolation
		if a.cfg.Snake.Domain != "flux" && a.cfg.Snake.Domain != "" {
			subjects = append(subjects,
				fmt.Sprintf("%s.msg.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.agent.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.rack.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.ctrl.>", a.cfg.Snake.Domain),
			)
		}
	}

	snakeSrv, err := snake.NewServer(ctx, snake.Config{
		Port:           a.cfg.Snake.Port,
		StoreDir:       filepath.Join(a.cfg.Store.Dir, "snake"),
		StreamName:     a.cfg.Snake.StreamName,
		StreamSubjects: subjects,
		TLSCert:        a.cfg.Snake.TLSCertFile,
		TLSKey:         a.cfg.Snake.TLSKeyFile,
		LogLevel:       a.cfg.Logging.Level,
	})
	if err != nil {
		return fmt.Errorf("failed to start snake: %w", err)
	}
	defer snakeSrv.Shutdown()

	// 3b. Provision Telemetry Stream
	if a.cfg.Observability.Enabled {
		telSubjects := []string{
			a.cfg.Telemetry.BaseSubject + ".>",
		}
		if errTel := snakeSrv.ProvisionStream(ctx, a.cfg.Telemetry.StreamName, telSubjects); errTel != nil {
			a.log.Warn("Failed to provision telemetry stream", "error", errTel)
		}
	}

	// 4. Discovery
	jsURL := a.cfg.Snake.URL
	if jsURL == "" || strings.HasSuffix(jsURL, ":0") {
		jsURL = snakeSrv.ClientURL()
	}

	// Internal Optimization: Mixer connects to its own Snake via plain protocol
	if strings.HasPrefix(jsURL, "tls://") && (strings.Contains(jsURL, "localhost") || strings.Contains(jsURL, "127.0.0.1") || strings.Contains(jsURL, "0.0.0.0")) {
		a.log.Info("Internal Bus connection detected, bypassing TLS for performance", "url", jsURL)
		jsURL = strings.Replace(jsURL, "tls://", "nats://", 1)
	}

	// 5. Bus Initialization (The Mesh)
	natsBus := bus.NewNatsBus(a.cfg.Snake.StreamName)
	connectTimeout, _ := time.ParseDuration(a.cfg.Snake.ConnectTimeout)
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	reconnectWait, _ := time.ParseDuration(a.cfg.Snake.ReconnectWait)
	if reconnectWait == 0 {
		reconnectWait = 1 * time.Second
	}

	inactiveThreshold, _ := time.ParseDuration(a.cfg.Snake.InactiveThreshold)
	if inactiveThreshold == 0 {
		inactiveThreshold = 30 * time.Second
	}

	if errConn := natsBus.Connect(jsURL, bus.ConnectOptions{
		Name:               a.cfg.Base.Name,
		Domain:             a.cfg.Snake.Domain,
		ConnectTimeout:     connectTimeout,
		ReconnectWait:      reconnectWait,
		InactiveThreshold:  inactiveThreshold,
		RootCA:             a.cfg.Snake.RootCAFile,
		InsecureSkipVerify: a.cfg.Snake.InsecureSkipVerify,
		InProcessServer:    snakeSrv, // Optimized internal link
	}); errConn != nil {
		return fmt.Errorf("failed to connect to bus: %w", errConn)
	}
	defer natsBus.Close()

	// Instrumented Bus for metrics
	managedBus := telemetry.NewInstrumentedBus(natsBus, nil)

	// 6. Router Initialization (Control Signaling)
	wmLogger := loggerPkg.NewWatermillAdapter(a.log)
	r, err := router.NewRouter(wmLogger)
	if err != nil {
		return fmt.Errorf("failed to init router: %w", err)
	}

	// Configure JS Streams
	// Use configured subjects if available, otherwise use defaults
	msgSubjects := a.cfg.Snake.StreamSubjects
	if len(msgSubjects) == 0 {
		msgSubjects = []string{
			"flux.msg.>",
			"flux.agent.>",
			"flux.rack.>",
			"flux.ctrl.>",
		}
		if a.cfg.Snake.Domain != "flux" && a.cfg.Snake.Domain != "" {
			msgSubjects = append(msgSubjects,
				fmt.Sprintf("%s.msg.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.agent.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.rack.>", a.cfg.Snake.Domain),
				fmt.Sprintf("%s.ctrl.>", a.cfg.Snake.Domain),
			)
		}
	}

	telemetrySubjects := a.cfg.Telemetry.StreamSubjects
	if len(telemetrySubjects) == 0 {
		telemetrySubjects = []string{
			a.cfg.Telemetry.BaseSubject + ".>",
		}
	}

	if errCfg := r.ConfigureJetStream(
		jsURL,
		a.cfg.Snake.Domain,
		true,
		a.cfg.Snake.RootCAFile,
		a.cfg.Snake.StreamName,
		msgSubjects,
		a.cfg.Telemetry.StreamName,
		telemetrySubjects,
		"24h", "24h",
		wmLogger,
	); errCfg != nil {
		return fmt.Errorf("failed to configure router: %w", errCfg)
	}

	// 7. Controller Initialization
	pushDelay, _ := time.ParseDuration(a.cfg.Enrollment.PushDelay)
	enrollCtrl := controller.NewEnrollmentController(a.log, store, r.Pub, clusterKey, mixerState.MachineID, pushDelay)
	enrollCtrl.RegisterRoutes(r)

	// Scenario Controller
	scenarioWait, _ := time.ParseDuration(a.cfg.Mixer.ScenarioWaitTimeout)
	if scenarioWait == 0 {
		scenarioWait = 15 * time.Second
	}
	scenarioCtrl := controller.NewScenarioController(a.log, a.cfg.Store.Dir, store, idGen, mixerState.MachineID, scenarioWait)
	scenarioCtrl.SetBus(managedBus)
	enrollCtrl.SetScenario(scenarioCtrl)

	// Metrics Cache
	mCache := telemetry.NewMetricsCache()

	// 7b. Telemetry Ingestion (Sink)
	// IMPORTANT: The sink must listen on the telemetry stream, not the message stream.
	telBusIngest := bus.NewNatsBus(a.cfg.Telemetry.StreamName)
	if errTelConn := telBusIngest.Connect(jsURL, bus.ConnectOptions{
		Name:               a.cfg.Base.Name + "-tel-ingest",
		Domain:             a.cfg.Snake.Domain,
		ConnectTimeout:     connectTimeout,
		ReconnectWait:      reconnectWait,
		RootCA:             a.cfg.Snake.RootCAFile,
		InsecureSkipVerify: a.cfg.Snake.InsecureSkipVerify,
		InProcessServer:    snakeSrv,
	}); errTelConn != nil {
		a.log.Error("Failed to connect telemetry ingestion bus", "error", errTelConn)
	} else {
		defer telBusIngest.Close()
		flushInterval, _ := time.ParseDuration(a.cfg.Telemetry.BatchInterval)
		if flushInterval == 0 {
			flushInterval = 5 * time.Second
		}
		sink := ingest.NewTelemetrySink(telBusIngest, store, a.cfg.Telemetry.BaseSubject+".>", a.cfg.Store.Dir, flushInterval, mCache)
		if errSink := sink.Start(ctx); errSink != nil {
			a.log.Error("Failed to start telemetry sink", "error", errSink)
		}
		defer func() { _ = sink.Stop() }()

		// Flush early logs to NATS now that the sink is active
		if a.bufHandler != nil {
			nw := telemetry.NewNatsWriter(telBusIngest, mixerEntityID, mixerState.Name, a.cfg.Telemetry.BaseSubject, idGen, 5*time.Second)
			lLevel := loggerPkg.ParseLevel(a.cfg.Logging.Level)
			h := slog.NewJSONHandler(nw, &slog.HandlerOptions{Level: lLevel, AddSource: true})
			// Inject Component Identity for Ingestion
			hWithID := h.WithAttrs([]slog.Attr{
				slog.String("entity_type", string(loggerPkg.TypeMixer)),
				slog.String("component", string(loggerPkg.TypeMixer)),
			})

			// Combined: NATS + Base (STDOUT/File)
			combined := telemetry.NewMultiHandler(hWithID, baseLogger.Handler())
			a.switchHandler.Switch(combined)
			_ = a.bufHandler.FlushTo(ctx, combined)
		}
	}

	// 8. Register Mixer & Snake in local Registry
	a.log.Info("Registering Mixer and Snake identities...", "id", mixerState.MachineID)
	mixerIP := netutil.LocalIPv4()
	_, errReg := store.RegisterEntity(ctx, uint8(idgen.EntityMixer), mixerState.MachineID, mixerState.Name, a.cfg.Enrollment.BootstrapSecret, mixerIP, a.cfg.API.Port, version.Version, nil, nil, mixerState.MachineID)
	if errReg != nil {
		a.log.Error("Failed to register Mixer in local registry", "error", errReg)
	}

	snakeAttrs := map[string]any{
		"topology":   "rack",
		"mixer":      "true",
		"rack":       "true",
		"rack_ip":    mixerIP,
		"rack_port":  a.cfg.Snake.Port,
		"mixer_ip":   mixerIP,
		"mixer_port": a.cfg.API.Port,
	}
	_, errSnk := store.RegisterEntity(ctx, uint8(idgen.EntitySnake), mixerState.MachineID, "embedded-snake", a.cfg.Enrollment.BootstrapSecret, mixerIP, a.cfg.Snake.Port, version.Version, nil, snakeAttrs, mixerState.MachineID)
	if errSnk != nil {
		a.log.Error("Failed to register Snake in local registry", "error", errSnk)
	}

	// Initial Heartbeat for Mixer to populate stats
	initialStats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"uptime":     "0s",
	}
	if errHb := store.HeartbeatEntity(ctx, uint8(idgen.EntityMixer), mixerState.MachineID, initialStats, nil); errHb != nil {
		a.log.Warn("Failed to send initial heartbeat for Mixer", "error", errHb)
	}

	// Initial Heartbeat for Snake
	snakeStats := map[string]any{
		"in_msgs":  0,
		"out_msgs": 0,
	}
	if errHbSnk := store.HeartbeatEntity(ctx, uint8(idgen.EntitySnake), mixerState.MachineID, snakeStats, nil); errHbSnk != nil {
		a.log.Warn("Failed to send initial heartbeat for Snake", "error", errHbSnk)
	}

	// 8b. Wasm Catalog Initialization
	if errProv := snakeSrv.ProvisionKV(ctx, "wasm_catalog"); errProv != nil {
		a.log.Warn("Failed to provision Wasm KV store", "error", errProv)
	}

	// We pass natsBus.KV() directly as the Store. It matches the Put() interface of catalog.Store
	wasmCat, errCat := catalog.NewCatalogManager(a.log, a.cfg.Wasm.CatalogDir, a.cfg.Wasm.TrustedKeysDir, clusterKey, natsBus.KV())
	if errCat != nil {
		return fmt.Errorf("failed to init wasm catalog: %w", errCat)
	}

	// 9. API Server
	apiSrv := api.NewServer(store, r.Pub, clusterKey, scenarioCtrl, mCache, mixerState.MachineID, mixerEntityID, a.cfg, wasmCat)
	go func() {
		addr := fmt.Sprintf(":%d", a.cfg.API.Port)
		if errSrv := apiSrv.Start(addr); errSrv != nil {
			a.log.Error("API server failed", "error", errSrv)
		}
	}()

	a.log.Info("Mixer is ready", "name", mixerState.Name, "id", mixerState.MachineID, "entity_id", mixerEntityID)

	// 10. Startup Scenario Execution
	if a.scenarioRef != "" {
		a.log.Info("Executing startup scenario", "ref", a.scenarioRef)
		// 1. Resolve Scenario (File path or Name)
		var content []byte
		var errRead error
		if _, errStat := os.Stat(a.scenarioRef); errStat == nil {
			// It's a file path
			content, errRead = os.ReadFile(a.scenarioRef)
			if errRead != nil {
				a.log.Warn("Failed to read startup scenario file", "path", a.scenarioRef, "error", errRead)
			}
		}

		if len(content) > 0 {
			// Import it
			name, errImp := scenarioCtrl.Import(ctx, content, false)
			if errImp != nil {
				a.log.Warn("Failed to import startup scenario", "error", errImp)
			} else {
				// Activate it
				if errAct := scenarioCtrl.Activate(ctx, name); errAct != nil {
					a.log.Warn("Failed to activate startup scenario", "name", name, "error", errAct)
				}
			}
		} else {
			// Assume it's a name already in the repo
			if errAct := scenarioCtrl.Activate(ctx, a.scenarioRef); errAct != nil {
				a.log.Warn("Failed to activate named startup scenario", "name", a.scenarioRef, "error", errAct)
			}
		}
	}

	// 9. Start Router
	if errRun := r.Router.Run(ctx); errRun != nil {
		return fmt.Errorf("router failed: %w", errRun)
	}

	return nil
}
