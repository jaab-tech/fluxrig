// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/mixer/api"
	"github.com/jaab-tech/fluxrig/pkg/netutil"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry/kvstore"
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
	// ready is closed once Run has finished startup: store migrated, bus and
	// controllers up, API server started.
	ready     chan struct{}
	readyOnce sync.Once
}

// NewApp creates a new Mixer application instance.
func NewApp(cfg *config.MixerConfig, scenarioRef string, bufHandler *telemetry.BufferHandler) *App {
	return &App{
		cfg:         cfg,
		scenarioRef: scenarioRef,
		log:         slog.Default().With("component", "MIXER"),
		bufHandler:  bufHandler,
		ready:       make(chan struct{}),
	}
}

// Ready is closed when Run has finished startup. A caller that must not
// interrupt startup (a test, a supervisor) waits on it instead of on a timer.
func (a *App) Ready() <-chan struct{} {
	return a.ready
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
	subjects := messageSubjects(&a.cfg.Snake)

	storeKey, storeOldKey, storeKeySource, errStoreKey := resolveStoreKeys(&a.cfg.Snake, clusterKey)
	if errStoreKey != nil {
		return fmt.Errorf("failed to resolve the snake store key: %w", errStoreKey)
	}
	streamMaxAge, errMaxAge := parseStreamMaxAge(a.cfg.Snake.StreamMaxAge)
	if errMaxAge != nil {
		return errMaxAge
	}
	kvTTL, errKVTTL := parseKVTTL(a.cfg.Snake.KVTTL)
	if errKVTTL != nil {
		return errKVTTL
	}
	if storeKey == "" {
		a.log.Warn("Snake store encryption is OFF: every message on a wire rests in clear on this disk", "setting", "snake.store_encryption")
	} else {
		a.log.Info("Snake store is encrypted at rest", "cipher", a.cfg.Snake.StoreCipher, "key", storeKeySource)
	}
	if a.cfg.Snake.AllowNonTLS && a.cfg.Snake.TLSCertFile != "" {
		a.log.Warn("Snake accepts plaintext clients alongside TLS ones", "setting", "snake.allow_non_tls")
	}

	snakeStoreDir := a.cfg.Snake.StoreDir
	if snakeStoreDir == "" {
		snakeStoreDir = filepath.Join(a.cfg.Store.Dir, "snake")
	}
	snakeSrv, err := snake.NewServer(ctx, snake.Config{
		Port:           a.cfg.Snake.Port,
		ClusterName:    a.cfg.Snake.ClusterName,
		StoreDir:       snakeStoreDir,
		StreamName:     a.cfg.Snake.StreamName,
		StreamSubjects: subjects,
		TLSCert:        a.cfg.Snake.TLSCertFile,
		TLSKey:         a.cfg.Snake.TLSKeyFile,
		TLSCA:          a.cfg.Snake.TLSCAFile,
		TLSVerify:      a.cfg.Snake.TLSVerify,
		AllowNonTLS:    a.cfg.Snake.AllowNonTLS,
		LogLevel:       a.cfg.Logging.Level,
		StoreKey:       storeKey,
		StoreOldKey:    storeOldKey,
		StoreCipher:    a.cfg.Snake.StoreCipher,
		StreamMaxAge:   streamMaxAge,
		StreamMaxBytes: a.cfg.Snake.StreamMaxBytes,
		KVMaxBytes:     a.cfg.Snake.KVMaxBytes,
		KVMaxValueSize: a.cfg.Snake.KVMaxValueSize,
		KVTTL:          kvTTL,
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

	// 3c. Provision the Dead Letter Stream. A message no consumer could process
	// is republished here instead of disappearing, so it must exist before any
	// consumer can need it. Its own max age keeps a poison message from living
	// forever and filling the disk.
	if a.cfg.Snake.DLQEnabled {
		dlqPrefix := a.cfg.Snake.DLQSubject
		if dlqPrefix == "" {
			dlqPrefix = "flux.dlq"
		}
		dlqSubjects := []string{dlqPrefix + ".>"}
		if errDLQ := snakeSrv.ProvisionStreamWithAge(ctx, "flux-dlq", dlqSubjects, a.cfg.Snake.DLQMaxAge); errDLQ != nil {
			a.log.Warn("Failed to provision dead letter stream", "error", errDLQ)
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
		DLQEnabled:         a.cfg.Snake.DLQEnabled,
		DLQSubject:         a.cfg.Snake.DLQSubject,
	}); errConn != nil {
		return fmt.Errorf("failed to connect to bus: %w", errConn)
	}
	defer natsBus.Close()

	// Instrumented Bus for metrics
	managedBus := telemetry.NewInstrumentedBus(natsBus, nil)

	// 5b. Registry: Racks, names, scenario files and the active pointer in a
	// JetStream key-value bucket, presence in a second one that expires it.
	regCfg := a.cfg.Registry.WithDefaults()
	livenessTTL, errTTL := time.ParseDuration(regCfg.LivenessTTL)
	if errTTL != nil || livenessTTL <= 0 {
		return fmt.Errorf("registry.liveness_ttl must be a positive duration, got %q", regCfg.LivenessTTL)
	}
	regBucket, liveBucket, errBuckets := kvstore.EnsureBuckets(ctx, natsBus.JetStream(), kvstore.BucketConfig{
		Registry:     regCfg.Bucket,
		Liveness:     regCfg.LivenessBucket,
		Replicas:     regCfg.Replicas,
		MaxValueSize: regCfg.MaxValueSize,
		LivenessTTL:  livenessTTL,
	})
	if errBuckets != nil {
		return fmt.Errorf("failed to open the registry: %w", errBuckets)
	}
	reg := kvstore.New(a.log, regBucket, liveBucket, store, mixerState.MachineID, kvstore.Config{
		MaxValueSize: regCfg.MaxValueSize,
		WriteRetries: regCfg.WriteRetries,
	})
	a.log.Info("Setting auto_adopt", "enabled", a.cfg.Enrollment.AutoAdopt)
	reg.SetAutoAdopt(a.cfg.Enrollment.AutoAdopt)
	reg.SetBootstrapSecret(a.cfg.Enrollment.BootstrapSecret)

	// Repair what a crash between two registry writes left behind: once now,
	// then on an interval. Validated at load; WithDefaults covers a config
	// built in code.
	sweepInterval, _ := time.ParseDuration(regCfg.SweepInterval)
	orphanGrace, _ := time.ParseDuration(regCfg.OrphanMinAge)
	if sweepInterval > 0 && orphanGrace > 0 {
		go a.sweepRegistry(ctx, reg, sweepInterval, orphanGrace)
	}

	// 6. Router Initialization (Control Signaling)
	wmLogger := loggerPkg.NewWatermillAdapter(a.log)
	r, err := router.NewRouter(wmLogger)
	if err != nil {
		return fmt.Errorf("failed to init router: %w", err)
	}

	// Configure JS Streams
	// Use configured subjects if available, otherwise use defaults
	msgSubjects := messageSubjects(&a.cfg.Snake)

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
	enrollCtrl := controller.NewEnrollmentController(a.log, reg, r.Pub, clusterKey, mixerState.MachineID, pushDelay)
	enrollCtrl.RegisterRoutes(r)

	// Scenario Controller
	scenarioWait, _ := time.ParseDuration(a.cfg.Mixer.ScenarioWaitTimeout)
	if scenarioWait == 0 {
		scenarioWait = 15 * time.Second
	}
	scenarioCtrl := controller.NewScenarioController(a.log, store, reg, reg, idGen, mixerState.MachineID, scenarioWait)
	scenarioCtrl.SetBus(managedBus)

	// The store the Mixer resolves a scenario's spec references against, so the
	// artefacts travel to the racks with the scenario that names them. The API
	// files uploads into the same instance: a manager keeps its index in
	// memory, so a second one would not see what the first one stored.
	specMgr, errSpec := manager.NewManager(filepath.Join(a.cfg.Store.Dir, "store"))
	if errSpec != nil {
		a.log.Warn("spec store unavailable; scenarios will ship without the specs they name", "error", errSpec)
	} else {
		scenarioCtrl.WithSpecStore(specMgr)
	}
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

		// Retention sweep over the parquet files the sink writes. Started
		// here, beside the sink, because it cleans up what the sink
		// produces and shares its data directory.
		retentionDays, checkInterval := a.retentionPolicy()
		janitor := telemetry.NewJanitor(a.log, a.cfg.Store.Dir, retentionDays, checkInterval)
		janitor.Start(ctx)
		a.log.Info("Telemetry retention janitor started",
			"retention_days", retentionDays,
			"check_interval", checkInterval)

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
	errReg := reg.UpsertSelf(ctx, uint8(idgen.EntityMixer), mixerState.MachineID, mixerState.Name, mixerIP, a.cfg.API.Port, version.Version, nil)
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
	errSnk := reg.UpsertSelf(ctx, uint8(idgen.EntitySnake), mixerState.MachineID, "embedded-snake", mixerIP, a.cfg.Snake.Port, version.Version, snakeAttrs)
	if errSnk != nil {
		a.log.Error("Failed to register Snake in local registry", "error", errSnk)
	}

	// Initial Heartbeat for Mixer to populate stats
	initialStats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"uptime":     "0s",
	}
	if errHb := reg.HeartbeatEntity(ctx, uint8(idgen.EntityMixer), mixerState.MachineID, initialStats, nil); errHb != nil {
		a.log.Warn("Failed to send initial heartbeat for Mixer", "error", errHb)
	}

	// Initial Heartbeat for Snake
	snakeStats := map[string]any{
		"in_msgs":  0,
		"out_msgs": 0,
	}
	if errHbSnk := reg.HeartbeatEntity(ctx, uint8(idgen.EntitySnake), mixerState.MachineID, snakeStats, nil); errHbSnk != nil {
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
	apiSrv := api.NewServer(reg, r.Pub, clusterKey, scenarioCtrl, mCache, mixerState.MachineID, mixerEntityID, a.cfg, wasmCat, r.NC)
	if errSpec == nil {
		apiSrv.WithSpecStore(specMgr)
	}
	// The API is how the Mixer is managed and how artefacts reach its store.
	// A Mixer without it looks healthy and cannot be operated, so a missing
	// token or a port that cannot be bound stops the start, and an API that
	// fails later stops the Mixer.
	apiListener, errListen := apiSrv.Listen(fmt.Sprintf(":%d", a.cfg.API.Port))
	if errListen != nil {
		return errListen
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	apiErr := make(chan error, 1)
	go func() {
		errSrv := apiSrv.Serve(apiListener)
		a.log.Error("API server failed", "error", errSrv)
		apiErr <- errSrv
		stopRun()
	}()

	a.log.Info("Mixer is ready", "name", mixerState.Name, "id", mixerState.MachineID, "entity_id", mixerEntityID)
	a.readyOnce.Do(func() { close(a.ready) })

	// 10. Startup Scenario Execution. --scenario names the scenario to run;
	// without it, the one the registry last recorded as active is restored,
	// without pushing it again to Racks that already run it.
	if a.scenarioRef == "" {
		if errLoad := scenarioCtrl.LoadActive(ctx); errLoad != nil {
			a.log.Warn("Failed to restore the active scenario", "error", errLoad)
		}
	}
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
					logStartupActivateResult(a.log, name, errAct)
				}
			}
		} else {
			// Assume it's a name already in the repo
			if errAct := scenarioCtrl.Activate(ctx, a.scenarioRef); errAct != nil {
				logStartupActivateResult(a.log, a.scenarioRef, errAct)
			}
		}
	}

	// 9. Start Router
	if errRun := r.Router.Run(runCtx); errRun != nil {
		return fmt.Errorf("router failed: %w", errRun)
	}
	select {
	case errSrv := <-apiErr:
		return fmt.Errorf("mixer API stopped: %w", errSrv)
	default:
	}
	return nil
}

// retentionPolicy resolves the retention sweep settings. RetentionDays wins
// over Rotation.MaxAgeDays when it is positive, so the two keys never disagree
// silently about the same value. The interval is the rotation check interval.
func (a *App) retentionPolicy() (int, time.Duration) {
	days := a.cfg.Observability.Embedded.RetentionDays
	if days <= 0 {
		days = a.cfg.Observability.Embedded.Rotation.MaxAgeDays
	}
	interval, err := time.ParseDuration(a.cfg.Observability.Embedded.Rotation.CheckInterval)
	if err != nil || interval <= 0 {
		interval = time.Hour
	}
	return days, interval
}

// logStartupActivateResult reports how Activate failed for a startup scenario.
// ErrNoRackReached is the ordinary case at boot: no Rack has enrolled yet when
// this runs, and the scenario is still the Mixer's active one, reaching each
// Rack as it enrolls (Activate's own comment on this). Logging that at Warn
// would print a false "activation failed" on nearly every cold start that
// pins a deploy target; anything else is a real failure and stays a Warn.
func logStartupActivateResult(log *slog.Logger, name string, err error) {
	if errors.Is(err, controller.ErrNoRackReached) {
		log.Info("Startup scenario activated; no Rack has enrolled yet", "name", name, "detail", err)
		return
	}
	log.Warn("Failed to activate startup scenario", "name", name, "error", err)
}

// sweepRegistry runs the registry sweep at startup and then every interval,
// until ctx ends.
func (a *App) sweepRegistry(ctx context.Context, reg *kvstore.Store, interval, grace time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if res, err := reg.SweepOrphans(ctx, grace); err != nil {
			a.log.Warn("registry sweep failed", "error", err)
		} else if res != (kvstore.SweepResult{}) {
			a.log.Info("registry sweep repaired entries",
				"removed", res.Removed, "repaired", res.Repaired, "name_keys_removed", res.NameKeysRemoved)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
