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
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/events"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/ingest"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/logger/rotator"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/mixer/api"
	"github.com/jaab-tech/fluxrig/pkg/netutil"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/registry/forward"
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
	baseHandler   slog.Handler
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

// setupLogging wires stdout (or a rotating file) through the switchable
// handler so a later stage can tee the log onto the bus.
func (a *App) setupLogging() {
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
	a.baseHandler = baseLogger.Handler()
	a.switchHandler = telemetry.NewSwitchableHandler(a.baseHandler)
	a.bufHandler = telemetry.NewBufferHandler(telemetry.NewSourceHandler(a.switchHandler))
	a.log = slog.New(a.bufHandler)
}

// Run initializes and starts the Mixer service.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Unified Logger Setup
	a.setupLogging()

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
	a.logStoreEncryption(storeKey, storeKeySource)
	if a.cfg.Snake.AllowNonTLS && a.cfg.Snake.TLSCertFile != "" {
		a.log.Warn("Snake accepts plaintext clients alongside TLS ones", "setting", "snake.allow_non_tls")
	}

	snakeStoreDir := a.cfg.Snake.StoreDir
	if snakeStoreDir == "" {
		snakeStoreDir = filepath.Join(a.cfg.Store.Dir, "snake")
	}
	if a.cfg.Snake.External {
		a.log.Info("Snake is external: no embedded NATS starts", "url", a.cfg.Snake.URL)
		// Credentials authenticate the Mixer, but without TLS anyone on the
		// path reads and changes the traffic: control writes, Passports, the
		// registry. snake.url is comma-separated, like the helper takes.
		if externalNATSUsesPlaintext(a.cfg.Snake.URL) {
			a.log.Warn("the external NATS URL is not TLS: everything on the wire travels in plaintext", "setting", "snake.url")
		}
		warnWorldReadable(a.log, a.cfg.Snake.RootCAFile, a.cfg.Snake.ClientCertFile,
			a.cfg.Snake.ClientKeyFile, a.cfg.Snake.CredentialsFile)
	}

	// Validated at load; WithDefaults covers a config built in code, where an
	// empty value means "take the server's own default".
	formTimeout, _ := time.ParseDuration(a.cfg.Snake.FormTimeout)
	// One stream provisioning attempt is one bus operation. Zero (unset)
	// keeps the server's own bound.
	provisionTimeout, _ := time.ParseDuration(a.cfg.Snake.OperationTimeout)
	snakeCfg := snake.Config{
		Port:             a.cfg.Snake.Port,
		ClusterName:      a.cfg.Snake.ClusterName,
		StoreDir:         snakeStoreDir,
		StreamName:       a.cfg.Snake.StreamName,
		StreamSubjects:   subjects,
		TLSCert:          a.cfg.Snake.TLSCertFile,
		TLSKey:           a.cfg.Snake.TLSKeyFile,
		TLSCA:            a.cfg.Snake.TLSCAFile,
		TLSVerify:        a.cfg.Snake.TLSVerify,
		AllowNonTLS:      a.cfg.Snake.AllowNonTLS,
		LogLevel:         a.cfg.Logging.Level,
		StoreKey:         storeKey,
		StoreOldKey:      storeOldKey,
		StoreCipher:      a.cfg.Snake.StoreCipher,
		StreamMaxAge:     streamMaxAge,
		StreamMaxBytes:   a.cfg.Snake.StreamMaxBytes,
		KVMaxBytes:       a.cfg.Snake.KVMaxBytes,
		KVMaxValueSize:   a.cfg.Snake.KVMaxValueSize,
		KVTTL:            kvTTL,
		Replicas:         a.cfg.Snake.Replicas,
		ProvisionTimeout: provisionTimeout,
		FormTimeout:      formTimeout,
	}
	snakeSrv, err := a.startSnake(ctx, snakeCfg)
	if err != nil {
		return fmt.Errorf("failed to start snake: %w", err)
	}
	defer snakeSrv.Shutdown()
	// The bus connects through the embedded server when there is one.
	var inProcess any
	if !a.cfg.Snake.External {
		inProcess = snakeSrv
	}

	// 3b. Provision the streams beside the message stream.
	a.provisionSideStreams(ctx, snakeSrv)

	// 4. Discovery
	jsURL := a.cfg.Snake.URL
	if jsURL == "" || strings.HasSuffix(jsURL, ":0") {
		jsURL = snakeSrv.ClientURL()
	}

	// Internal Optimization: Mixer connects to its own Snake via plain protocol
	if !a.cfg.Snake.External && strings.HasPrefix(jsURL, "tls://") && (strings.Contains(jsURL, "localhost") || strings.Contains(jsURL, "127.0.0.1") || strings.Contains(jsURL, "0.0.0.0")) {
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

	// Empty, zero or negative leaves the bus default in place. A value that
	// is not a duration stops the start rather than reading as zero.
	var dlqPublishTimeout time.Duration
	if v := a.cfg.Snake.DLQPublishTimeout; v != "" {
		parsed, errDLQTimeout := time.ParseDuration(v)
		if errDLQTimeout != nil {
			return fmt.Errorf("invalid snake.dlq_publish_timeout %q: %w", v, errDLQTimeout)
		}
		dlqPublishTimeout = parsed
	}

	if errConn := natsBus.Connect(jsURL, bus.ConnectOptions{
		Name:               a.cfg.Base.Name,
		Domain:             a.cfg.Snake.Domain,
		ConnectTimeout:     connectTimeout,
		ReconnectWait:      reconnectWait,
		InactiveThreshold:  inactiveThreshold,
		RootCA:             a.cfg.Snake.RootCAFile,
		ClientCert:         a.cfg.Snake.ClientCertFile,
		ClientKey:          a.cfg.Snake.ClientKeyFile,
		Credentials:        a.cfg.Snake.CredentialsFile,
		PingInterval:       pingInterval(&a.cfg.Snake),
		MaxPingsOut:        a.cfg.Snake.MaxPingsOut,
		AckWait:            ackWait(&a.cfg.Snake),
		External:           a.cfg.Snake.External,
		InsecureSkipVerify: a.cfg.Snake.InsecureSkipVerify,
		InProcessServer:    inProcess, // Optimized internal link
		DLQEnabled:         a.cfg.Snake.DLQEnabled,
		DLQSubject:         a.cfg.Snake.DLQSubject,
		DLQPublishTimeout:  dlqPublishTimeout,
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
	leaseBucketName := ""
	var leaseTTL time.Duration
	if regCfg.LeaseEnabled {
		leaseTTL, _ = time.ParseDuration(regCfg.LeaseTTL) // validated at load
		leaseBucketName = regCfg.LeaseBucket
	}
	// The registry, liveness, lease and spec buckets take registry.replicas,
	// the streams snake.replicas. Set to 3 and left at 1, the registry stops
	// with the one NATS server that holds it while the streams go on.
	if a.cfg.Snake.External && regCfg.Replicas < a.cfg.Snake.Replicas {
		a.log.Warn("registry.replicas is below snake.replicas: the registry, the lease and the specs have fewer copies than the streams",
			"registry_replicas", regCfg.Replicas, "snake_replicas", a.cfg.Snake.Replicas)
	}
	regBucket, liveBucket, leaseBucket, errBuckets := kvstore.EnsureBuckets(ctx, natsBus.JetStream(), kvstore.BucketConfig{
		Registry:     regCfg.Bucket,
		Liveness:     regCfg.LivenessBucket,
		Replicas:     regCfg.Replicas,
		MaxValueSize: regCfg.MaxValueSize,
		LivenessTTL:  livenessTTL,
		LeaseBucket:  leaseBucketName,
		LeaseTTL:     leaseTTL,
	})
	if errBuckets != nil {
		return fmt.Errorf("failed to open the registry: %w", errBuckets)
	}
	// Several Mixers sign passports for the same Racks, and a Rack refuses a
	// passport signed by a key other than the one it pinned. With the lease
	// on, refuse to start with a cluster.key the rest of the cluster does not
	// use, instead of failing each Rack at its next failover.
	if regCfg.LeaseEnabled {
		if errKey := kvstore.CheckClusterKey(ctx, regBucket, clusterKey.Public); errKey != nil {
			return fmt.Errorf("mixer: %w (%s)", errKey, keyPath)
		}
	}
	reg := kvstore.New(a.log, regBucket, liveBucket, store, mixerState.MachineID, kvstore.Config{
		MaxValueSize: regCfg.MaxValueSize,
		WriteRetries: regCfg.WriteRetries,
	})

	// The write lease: one Mixer holds it and does every control-plane write.
	// Off by default, which keeps a single Mixer exactly as it was.
	var lease *kvstore.Lease
	// leaseAcquired wakes the spec migration each time this Mixer takes the
	// lease. It holds one signal: a migration that is already due needs no
	// second one.
	leaseAcquired := make(chan struct{}, 1)
	// leaseStats counts the lease transitions for the Mixer's metrics.
	leaseStats := &leaseMetrics{}
	if regCfg.LeaseEnabled {
		leaseRenew, _ := time.ParseDuration(regCfg.LeaseRenew) // validated at load
		lease = kvstore.NewLease(leaseBucket, mixerState.MachineID, kvstore.LeaseConfig{
			TTL:   leaseTTL,
			Renew: leaseRenew,
		}, func() *slog.Logger { return a.log }, func(event string) {
			a.log.Debug("write lease event", "event", event, "mixer", mixerState.MachineID)
			leaseStats.record(event)
			if event == "acquired" {
				select {
				case leaseAcquired <- struct{}{}:
				default:
				}
			}
		})
		reg.SetLease(lease)
		// The renew loop stops before the release: if it were still running,
		// its next tick would find the lease not held and take it back.
		leaseCtx, stopLease := context.WithCancel(ctx)
		leaseDone := make(chan struct{})
		go func() {
			defer close(leaseDone)
			a.holdLease(leaseCtx, lease)
		}()
		defer func() {
			stopLease()
			// A renew in flight is bounded by the NATS request timeout; the
			// TTL bounds the wait in any case.
			select {
			case <-leaseDone:
			case <-time.After(lease.TTL()):
				a.log.Warn("write lease renew loop did not stop; releasing anyway")
			}
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), lease.TTL())
			defer cancelRelease()
			lease.Release(releaseCtx)
		}()
	}

	// The spec store. With the write lease on, every Mixer shares one in the
	// spec bucket: a spec imported through any Mixer is pushed by all of them. A single Mixer keeps its local
	// store. An import is a control write, so the API sends it to the lease
	// holder; the scenario controller reads the store this Mixer has.
	specStore, sharedSpecs, errSpecs := a.openSpecStores(ctx, regCfg, lease, natsBus.JetStream(), leaseAcquired, mixerState.MachineID.String())
	if errSpecs != nil {
		return errSpecs
	}

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

	// regFace is what a control write goes through: the local store when this
	// Mixer holds the lease (or there is none), or a forwarder to the holder
	// when it does not. Reads and presence always use the local store. The
	// forwarder is wired after ConfigureJetStream, which opens the connection.
	// regAny is the registry.Registry every caller uses; regDir is the
	// RackDirectory the scenario controller uses (it needs GetRackByName,
	// which the forward decorator adds). With no lease both are the local
	// store.
	var regAny registry.Registry = reg
	// specImports is the store an API import goes through: the local or
	// shared store, or a forwarder to the lease holder.
	specImports := specStore
	var regDir controller.RackDirectory = reg
	var scenFace controller.ScenarioRepository = reg

	// Configure JS Streams
	// Use configured subjects if available, otherwise use defaults
	msgSubjects := messageSubjects(&a.cfg.Snake)

	telemetrySubjects := a.cfg.Telemetry.StreamSubjects
	if len(telemetrySubjects) == 0 {
		telemetrySubjects = []string{
			a.cfg.Telemetry.BaseSubject + ".>",
		}
	}

	queueGroup := ""
	if lease != nil {
		queueGroup = a.cfg.Mixer.QueueGroup
	}
	routerRootCA, routerOpts, errOpts := routerSecurity(&a.cfg.Snake)
	if errOpts != nil {
		return fmt.Errorf("snake: %w", errOpts)
	}
	if errCfg := r.ConfigureJetStream(router.JetStreamSetup{
		URL:               jsURL,
		Domain:            a.cfg.Snake.Domain,
		RootCA:            routerRootCA,
		ClientOptions:     routerOpts,
		Durable:           true,
		BusinessStream:    a.cfg.Snake.StreamName,
		BusinessSubjects:  msgSubjects,
		BusinessMaxAge:    "24h",
		TelemetryStream:   a.cfg.Telemetry.StreamName,
		TelemetrySubjects: telemetrySubjects,
		TelemetryMaxAge:   "24h",
		Replicas:          a.cfg.Snake.Replicas,
		QueueGroup:        queueGroup,
		AckWait:           ackWait(&a.cfg.Snake),
		FormTimeout:       formTimeout,
	}, wmLogger); errCfg != nil {
		return fmt.Errorf("failed to configure router: %w", errCfg)
	}

	// The forwarder and the holder-side server need the router's plain NATS
	// connection, which ConfigureJetStream opens: wired before it, both got a
	// nil connection, every forward failed and nothing answered.
	// leaseReadTimeout bounds the read of the lease key that /health makes.
	var leaseReadTimeout time.Duration
	if lease != nil {
		if r.NC == nil {
			return fmt.Errorf("mixer: control forwarding: the router has no NATS connection")
		}
		fwdTimeout, _ := time.ParseDuration(a.cfg.Mixer.ForwardTimeout)
		forwarder := forward.NewForwarder(forward.NewNATSTransport(r.NC), fwdTimeout, a.log)
		leaseReadTimeout = forwarder.Timeout()
		fwdReg := forward.NewRegistry(reg, forwarder, lease)
		regAny = fwdReg
		regDir = fwdReg
		scenFace = forward.NewScenario(reg, forwarder, lease)
		if sharedSpecs != nil {
			specImports = forward.NewSpecs(sharedSpecs, forwarder, lease)
		}
		// Every Mixer subscribes; only the lease holder answers.
		holderSrv := forward.NewServer(reg, reg, lease, a.log)
		if sharedSpecs != nil {
			holderSrv = holderSrv.WithSpecs(sharedSpecs)
		}
		if errSrv := holderSrv.Start(r.NC); errSrv != nil {
			return fmt.Errorf("failed to start the control-forward server: %w", errSrv)
		}
		defer holderSrv.Stop()
	}

	// 7. Controller Initialization
	pushDelay, _ := time.ParseDuration(a.cfg.Enrollment.PushDelay)
	enrollCtrl := controller.NewEnrollmentController(a.log, regAny, r.Pub, clusterKey, mixerState.MachineID, pushDelay)
	enrollCtrl.RegisterRoutes(r)

	// Scenario Controller
	scenarioWait, _ := time.ParseDuration(a.cfg.Mixer.ScenarioWaitTimeout)
	if scenarioWait == 0 {
		scenarioWait = 15 * time.Second
	}
	scenarioCtrl := controller.NewScenarioController(a.log, store, regDir, scenFace, idGen, mixerState.MachineID, scenarioWait)
	scenarioCtrl.SetBus(managedBus)

	// The store the Mixer resolves a scenario's spec references against, so the
	// artefacts travel to the racks with the scenario that names them.
	if specStore == nil {
		a.log.Warn("spec store unavailable; scenarios will ship without the specs they name")
	} else {
		scenarioCtrl.WithSpecStore(specStore)
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
		ClientCert:         a.cfg.Snake.ClientCertFile,
		ClientKey:          a.cfg.Snake.ClientKeyFile,
		Credentials:        a.cfg.Snake.CredentialsFile,
		PingInterval:       pingInterval(&a.cfg.Snake),
		MaxPingsOut:        a.cfg.Snake.MaxPingsOut,
		External:           a.cfg.Snake.External,
		InsecureSkipVerify: a.cfg.Snake.InsecureSkipVerify,
		InProcessServer:    inProcess,
	}); errTelConn != nil {
		a.log.Error("Failed to connect telemetry ingestion bus", "error", errTelConn)
	} else {
		defer telBusIngest.Close()
		flushInterval, _ := time.ParseDuration(a.cfg.Telemetry.BatchInterval)
		if flushInterval == 0 {
			flushInterval = 5 * time.Second
		}
		stopTelemetry, errSink := a.startTelemetryStore(ctx, telBusIngest, store, mCache, lease, flushInterval)
		if errSink != nil {
			a.log.Error("Failed to start the telemetry store", "type", a.cfg.Observability.Storage.Type, "error", errSink)
		}
		defer stopTelemetry()

		// Retention sweep over the parquet files the sink writes. Started
		// here, beside the sink, because it cleans up what the sink
		// produces and shares its data directory.
		retentionDays, checkInterval := a.retentionPolicy()
		janitor := telemetry.NewJanitor(a.log, a.cfg.Store.Dir, retentionDays, checkInterval)
		janitor.Start(ctx)
		a.log.Info("Telemetry retention janitor started",
			"retention_days", retentionDays,
			"check_interval", checkInterval)

		// The Mixer's own metrics go the way a Rack's do: through the
		// telemetry stream into this sink, so `fluxrig metrics` reads them.
		if !a.cfg.Telemetry.Disabled {
			mw := telemetry.NewNatsWriter(telBusIngest, mixerEntityID, mixerState.Name, a.cfg.Telemetry.BaseSubject, idGen, provisionTimeout)
			mp, errMP := telemetry.InitMeterProvider(telemetry.MeterConfig{
				Writer:         mw,
				ServiceName:    "fluxrig-mixer",
				ServiceVersion: version.Version,
				EntityID:       mixerEntityID,
				EntityName:     mixerState.Name,
				Component:      string(loggerPkg.TypeMixer),
				Interval:       flushInterval,
			})
			if errMP != nil {
				a.log.Error("Failed to start the Mixer metrics", "error", errMP)
			} else {
				defer func() {
					if errShut := telemetry.ShutdownMeterProvider(mp, flushInterval); errShut != nil {
						a.log.Warn("Mixer metrics did not flush at shutdown", "error", errShut)
					}
				}()
				if lease != nil {
					if errLM := leaseStats.register(mp.Meter("fluxrig/mixer"), lease.Held); errLM != nil {
						a.log.Error("Failed to register the write lease metrics", "error", errLM)
					}
				}
			}
		}

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

			// Combined: NATS + the current base writer (stdout or the rotating
			// file setup put in place).
			combined := telemetry.NewMultiHandler(hWithID, a.baseHandler)
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

	// 8b. Wasm Catalog Initialization: the catalog and the keyring live on the
	// bus, shared by every Mixer.
	for _, bucket := range []string{catalog.CatalogBucket, catalog.KeysBucket} {
		if errProv := snakeSrv.ProvisionKV(ctx, bucket); errProv != nil {
			a.log.Warn("Failed to provision Wasm KV store", "bucket", bucket, "error", errProv)
		}
	}
	wasmCat, errCat := catalog.NewCatalogManager(a.log, a.cfg.Wasm.CatalogDir, a.cfg.Wasm.TrustedKeysDir, clusterKey, natsBus.KV())
	if errCat != nil {
		return fmt.Errorf("failed to init wasm catalog: %w", errCat)
	}
	if errSeed := wasmCat.SeedKeyring(ctx); errSeed != nil {
		a.log.Warn("Failed to add this Mixer's trusted keys to the shared keyring", "dir", a.cfg.Wasm.TrustedKeysDir, "error", errSeed)
	}

	// 9. API Server
	apiSrv := api.NewServer(regAny, r.Pub, clusterKey, scenarioCtrl, mCache, mixerState.MachineID, mixerEntityID, a.cfg, wasmCat, r.NC)
	if specImports != nil {
		apiSrv.WithSpecStore(specImports)
	}
	if lease != nil {
		apiSrv.WithLease(lease, leaseReadTimeout)
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
		// A Mixer that does not hold the lease follows the holder's active
		// pointer, so every Mixer reports the same scenario. The holder moves
		// it; a follower converges on it. With no lease, nothing follows.
		if lease != nil {
			go scenarioCtrl.FollowActive(ctx, followerInterval(a.cfg.Mixer.FollowInterval))
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

// startSnake starts the embedded server, or connects to the external NATS.
func (a *App) startSnake(ctx context.Context, cfg snake.Config) (*snake.Server, error) {
	if !a.cfg.Snake.External {
		return snake.NewServer(ctx, cfg)
	}
	clientOpts, err := externalClientOptions(&a.cfg.Snake)
	if err != nil {
		return nil, err
	}
	return snake.NewExternal(ctx, cfg, a.cfg.Snake.URL, clientOpts...)
}

// logStoreEncryption says what protects the store at rest.
func (a *App) logStoreEncryption(storeKey, source string) {
	switch {
	case a.cfg.Snake.External:
		a.log.Info("Encryption at rest belongs to the operator of the external NATS")
	case storeKey == "":
		a.log.Warn("Snake store encryption is OFF: every message on a wire rests in clear on this disk", "setting", "snake.store_encryption")
	default:
		a.log.Info("Snake store is encrypted at rest", "cipher", a.cfg.Snake.StoreCipher, "key", source)
	}
}

// routerSecurity returns how the router connects: the embedded server takes a
// root CA, and an external NATS the client options, which carry it too.
func routerSecurity(cfg *config.SnakeConfig) (string, []nats.Option, error) {
	if !cfg.External {
		return cfg.RootCAFile, nil, nil
	}
	opts, err := externalClientOptions(cfg)
	return "", opts, err
}

// externalClientOptions is how every connection of this Mixer reaches an external
// NATS: the bus builds the same options from the same settings.
func externalClientOptions(cfg *config.SnakeConfig) ([]nats.Option, error) {
	return bus.Security{
		RootCA:             cfg.RootCAFile,
		ClientCert:         cfg.ClientCertFile,
		ClientKey:          cfg.ClientKeyFile,
		Credentials:        cfg.CredentialsFile,
		PingInterval:       pingInterval(cfg),
		MaxPingsOut:        cfg.MaxPingsOut,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}.Options()
}

// pingInterval is snake.ping_interval, validated at load; zero keeps the
// client default.
func pingInterval(cfg *config.SnakeConfig) time.Duration {
	d, _ := time.ParseDuration(cfg.PingInterval)
	return d
}

// ackWait is snake.ack_wait, validated at load.
func ackWait(cfg *config.SnakeConfig) time.Duration {
	d, _ := time.ParseDuration(cfg.AckWait)
	return d
}

// provisionSideStreams creates the streams beside the message stream. A failure
// is logged and the Mixer goes on: each of these serves one feature, and the
// message stream does not depend on them.
func (a *App) provisionSideStreams(ctx context.Context, snakeSrv *snake.Server) {
	// Telemetry.
	if a.cfg.Observability.Enabled {
		telSubjects := []string{
			a.cfg.Telemetry.BaseSubject + ".>",
		}
		if errTel := snakeSrv.ProvisionStream(ctx, a.cfg.Telemetry.StreamName, telSubjects); errTel != nil {
			a.log.Warn("Failed to provision telemetry stream", "error", errTel)
		}
	}

	// The dead letter stream. A message no consumer could process
	// is republished here instead of disappearing, so it must exist before any
	// consumer can need it. Its own max age keeps a poison message from living
	// forever and filling the disk.
	if a.cfg.Snake.DLQEnabled {
		dlqPrefix := a.cfg.Snake.DLQSubject
		if dlqPrefix == "" {
			dlqPrefix = "flux.dlq"
		}
		dlqSubjects := []string{dlqPrefix + ".>"}
		if errDLQ := snakeSrv.ProvisionStreamWithAge(ctx, snake.DLQStream, dlqSubjects, a.cfg.Snake.DLQMaxAge); errDLQ != nil {
			a.log.Warn("Failed to provision dead letter stream", "error", errDLQ)
		}
	}

	// The stream of durable events. It has its own subjects and
	// limits: a stream cannot overlap the message stream, and events are short
	// lived. Without it a Rack keeps its traffic and parks its events.
	if errEvents := snakeSrv.ProvisionStreamWithLimits(ctx, events.StreamName, eventSubjects(&a.cfg.Snake), eventsMaxAge(&a.cfg.Snake), a.cfg.Snake.EventsMaxBytes); errEvents != nil {
		a.log.Warn("Failed to provision the events stream", "error", errEvents)
	}
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
// holdLease keeps the write lease, renewing every RenewInterval, and gives it
// up when ctx ends. It acquires first: a Mixer that cannot take the lease
// still serves reads, but every control write is refused with
// ErrNotLeaseHolder until it can (the app-layer view is a 503).
func (a *App) holdLease(ctx context.Context, lease *kvstore.Lease) {
	if acquired, err := lease.Acquire(ctx); err != nil {
		a.log.Warn("write lease acquire failed", "error", err)
	} else if !acquired {
		a.log.Info("write lease held by another Mixer; control writes are refused here until it is free")
	} else {
		a.log.Info("this Mixer holds the write lease")
	}
	ticker := time.NewTicker(lease.RenewInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := lease.Renew(ctx); err != nil {
				a.log.Warn("write lease renew error", "error", err)
			}
		}
	}
}

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

// followerInterval reads mixer.follow_interval; a non-positive or unparsable
// value takes the 5s default.
func followerInterval(raw string) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return 5 * time.Second
}

// startTelemetryStore starts what keeps the telemetry of the Racks. With the
// local store it is the sink of this Mixer. With a shared one (s3, nats,
// otlp), every Mixer keeps the live metrics of its cache, and the Mixer that
// holds the lease is the only one that writes: nothing is kept twice, and a
// new holder goes on from the last batch the previous one confirmed.
func (a *App) startTelemetryStore(ctx context.Context, b *bus.NatsBus, store *duckdb.Store, mCache *telemetry.MetricsCache,
	lease *kvstore.Lease, flushInterval time.Duration) (func(), error) {
	subject := a.cfg.Telemetry.BaseSubject + ".>"
	sc := a.cfg.Observability.Storage
	if sc.Type == "" || sc.Type == config.StorageLocal {
		if a.cfg.Snake.External {
			a.log.Warn("telemetry is kept on each Mixer's disk: with several Mixers each one has a copy of its own, which misses what arrived while it was down",
				"setting", "observability.storage.type")
		}
		sink := ingest.NewTelemetrySink(b, store, subject, a.cfg.Store.Dir, flushInterval, mCache)
		if err := sink.Start(ctx); err != nil {
			return func() {}, err
		}
		return func() { _ = sink.Stop() }, nil
	}
	if lease == nil {
		return func() {}, fmt.Errorf("observability.storage.type %s needs registry.lease_enabled", sc.Type)
	}
	// Telemetry batches carry message-adjacent data: the Rack logs, metrics
	// and spans of the traffic the Mixers route. A shared store keeps them
	// where every Mixer — and the operator of the bucket or collector — reads
	// them, so treat the store as sensitive and ship to it encrypted (see the
	// per-type warnings where each committer is built).
	a.log.Warn("telemetry is kept in a shared store, which carries message-adjacent data",
		"type", sc.Type)
	commitTimeout, _ := time.ParseDuration(sc.CommitTimeout) // validated at load
	batchInterval, _ := time.ParseDuration(sc.BatchInterval)
	committer, remote, err := a.telemetryCommitterWhileForming(ctx, b, store, commitTimeout)
	if err != nil {
		return func() {}, err
	}
	live := ingest.NewCacheSink(b, subject, mCache)
	if err = live.Start(ctx); err != nil {
		a.log.Warn("the live metrics of this Mixer did not start", "error", err)
	}
	wctx, cancel := context.WithCancel(ctx)
	writer := &ingest.SharedSink{
		JS: b.JetStream(), Stream: a.cfg.Telemetry.StreamName, Subject: subject,
		Persist: ingest.NewTelemetrySink(b, store, subject, a.cfg.Store.Dir, flushInterval, nil),
		Store:   store, Commit: committer, Held: lease.Held,
		BatchInterval: batchInterval, CommitTimeout: commitTimeout, Log: a.log,
	}
	go writer.Run(wctx)
	if remote != nil {
		go a.sweepRemote(wctx, remote, lease)
	}
	a.log.Info("telemetry is kept in a store every Mixer shares", "type", sc.Type, "batch_interval", batchInterval)
	return func() { cancel(); _ = live.Stop() }, nil
}

// telemetryCommitterWhileForming is telemetryCommitter, tried again while an
// external NATS cannot answer yet, up to snake.form_timeout, as the streams
// and buckets are. On a first start across a slow link the object store and
// its index took longer than one request allows, the Mixer went on with no
// telemetry store, and a holder of the lease without one stored nothing.
func (a *App) telemetryCommitterWhileForming(ctx context.Context, b *bus.NatsBus, store *duckdb.Store, timeout time.Duration) (ingest.Committer, ingest.Remote, error) {
	formTimeout, _ := time.ParseDuration(a.cfg.Snake.FormTimeout) // validated at load
	deadline := time.Now().Add(formTimeout)
	for attempt := 1; ; attempt++ {
		committer, remote, err := a.telemetryCommitter(ctx, b, store, timeout)
		if err == nil || !a.cfg.Snake.External || !(bus.Unavailable(err) || bus.Disconnected(err)) || time.Now().After(deadline) {
			return committer, remote, err
		}
		a.log.Info("the telemetry store is waiting for the external NATS", "type", a.cfg.Observability.Storage.Type, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// telemetryCommitter builds the committer of the configured shared store and,
// for s3 and nats, the Remote it writes and every Mixer reads.
func (a *App) telemetryCommitter(ctx context.Context, b *bus.NatsBus, store *duckdb.Store, timeout time.Duration) (ingest.Committer, ingest.Remote, error) {
	sc := a.cfg.Observability.Storage
	var remote ingest.Remote
	switch sc.Type {
	case config.StorageS3:
		warnWorldReadable(a.log, sc.S3.AccessKeyFile, sc.S3.SecretKeyFile)
		// S3Remote requests SSE-S3 for each uploaded object. The service must
		// support the standard header and enforce its encryption policy.
		if sc.S3.Plaintext {
			a.log.Warn("telemetry is uploaded to S3 over plaintext HTTP: development only", "setting", "observability.storage.s3.plaintext")
		}
		access, err := readSecretFile(sc.S3.AccessKeyFile)
		if err != nil {
			return nil, nil, err
		}
		secret, err := readSecretFile(sc.S3.SecretKeyFile)
		if err != nil {
			return nil, nil, err
		}
		remote, err = ingest.NewS3Remote(ctx, ingest.S3Options{
			Endpoint: sc.S3.Endpoint, Bucket: sc.S3.Bucket, Prefix: sc.S3.Prefix, Region: sc.S3.Region,
			AccessKey: access, SecretKey: secret, Plaintext: sc.S3.Plaintext, CAFile: sc.S3.CAFile,
		})
		if err != nil {
			return nil, nil, err
		}
	case config.StorageNATS:
		var err error
		if remote, err = ingest.NewNATSRemote(ctx, b.JetStream(), sc.NATS.Bucket, a.cfg.Snake.Replicas); err != nil {
			return nil, nil, err
		}
	case config.StorageOTLP:
		warnWorldReadable(a.log, sc.OTLP.CAFile, sc.OTLP.HeadersFile)
		// The endpoint is used as-is: an http:// one posts every batch in
		// plaintext. The default example shows https; the HA harness uses a
		// local http:// collector, which is why this warns rather than refuses.
		if otlpUsesPlaintext(sc.OTLP.Endpoint) {
			a.log.Warn("the OTLP collector endpoint is plaintext HTTP: telemetry travels unencrypted", "setting", "observability.storage.otlp.endpoint")
		}
		client, headers, err := ingest.OTLPClient(sc.OTLP.CAFile, sc.OTLP.HeadersFile, timeout)
		if err != nil {
			return nil, nil, err
		}
		store.SetBeforeQuery(func(context.Context, string, time.Time, time.Time) error { return registry.ErrTelemetryElsewhere })
		return &ingest.OTLPCommitter{Store: store, Endpoint: sc.OTLP.Endpoint, Client: client, Headers: headers, Log: a.log}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown observability.storage.type %q", sc.Type)
	}
	// Files of a batch this Mixer wrote and never stored, before it stopped:
	// the next holder stores that batch again. Removed before the writer
	// starts, so no query reads them beside the stored copy.
	if n, err := ingest.RemoveUnstored(store.TelemetryDir()); err != nil {
		a.log.Warn("cannot remove the telemetry files this Mixer never stored", "error", err)
	} else if n > 0 {
		a.log.Info("removed telemetry files this Mixer wrote and never stored", "files", n)
	}
	cache := &ingest.Cache{Dir: store.TelemetryDir(), Remote: remote, Log: a.log}
	store.SetBeforeQuery(cache.Sync)
	return &ingest.ObjectCommitter{Store: store, Remote: remote, Log: a.log}, remote, nil
}

// sweepRemote removes the shared telemetry files older than the retention,
// on the lease holder, every rotation interval.
func (a *App) sweepRemote(ctx context.Context, remote ingest.Remote, lease *kvstore.Lease) {
	days, interval := a.retentionPolicy()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if lease.Held() {
			// Three days of hours past the retention: wider than any gap
			// between two sweeps a holder change leaves.
			ingest.SweepRemote(ctx, remote, time.Duration(days)*24*time.Hour, 72*time.Hour, a.log)
		}
	}
}

// readSecretFile returns the trimmed content of path, or "" for no path.
func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// warnWorldReadable logs a startup warning for each credential or key file
// anyone on the machine can read: a client certificate, key or credentials
// file the whole machine reads lets any local user speak to NATS as its
// owner. See bus.WorldReadable for what counts.
func warnWorldReadable(log *slog.Logger, paths ...string) {
	for _, p := range paths {
		if p != "" && bus.WorldReadable(p) {
			log.Warn("a credential or key file is readable by other users on this machine; restrict it to its owner", "path", p)
		}
	}
}

// externalNATSUsesPlaintext reports whether any URL in a comma-separated NATS
// URL list (snake.url) talks plaintext instead of TLS. Credentials
// authenticate the Mixer, but without TLS anyone on the path reads and
// changes the traffic, so a non-TLS entry warns loudly at startup.
func externalNATSUsesPlaintext(url string) bool {
	for _, u := range strings.Split(url, ",") {
		u = strings.ToLower(strings.TrimSpace(u))
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "tls://") {
			return true
		}
	}
	return false
}

// otlpUsesPlaintext reports whether the OTLP collector endpoint talks HTTP
// instead of HTTPS. The endpoint is used as-is for the /v1/logs, /v1/metrics
// and /v1/traces posts, so an http:// one sends every batch unencrypted.
func otlpUsesPlaintext(endpoint string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(endpoint)), "http://")
}

// openSpecStores returns the store a Mixer reads and the one an API import is
// sent through. With the write lease off both are the local store. With it on
// the shared JetStream store replaces both, the local store is migrated the
// first time this Mixer holds the lease, and the caller wires the import path
// through the lease holder.
func (a *App) openSpecStores(ctx context.Context, regCfg config.RegistryConfig, lease *kvstore.Lease, js jetstream.JetStream, acquired <-chan struct{}, mixerID string) (manager.Manager, *kvstore.SpecStore, error) {
	local, errLocal := manager.NewManager(filepath.Join(a.cfg.Store.Dir, "store"))
	if errLocal != nil {
		a.log.Warn("local spec store unavailable", "error", errLocal)
	}
	var store manager.Manager
	if local != nil {
		store = local
	}
	if lease == nil {
		return store, nil, nil
	}
	specBucket, err := kvstore.EnsureSpecBucket(ctx, js, kvstore.SpecBucketConfig{
		Name:     regCfg.SpecBucket,
		Replicas: regCfg.Replicas,
		MaxSize:  regCfg.SpecMaxSize,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open the spec store: %w", err)
	}
	shared := kvstore.NewSpecStore(specBucket, regCfg.SpecMaxSize, lease)
	go a.migrateSpecs(ctx, shared, local, acquired, mixerID)
	return shared, shared, nil
}

// migrateSpecs copies this Mixer's local spec store into the shared one the
// first time this Mixer holds the lease, if no holder did it before. It waits
// for each lease acquisition and stops once the cluster has migrated.
func (a *App) migrateSpecs(ctx context.Context, shared *kvstore.SpecStore, local manager.Manager, acquired <-chan struct{}, mixerID string) {
	if local == nil {
		return
	}
	if items, err := local.List(ctx); err == nil && len(items) > 0 {
		a.log.Info("specs are read from the shared store with the write lease on; this Mixer's local spec store is copied into it only if this Mixer is the first to hold the lease",
			"local_versions", len(items))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-acquired:
		}
		res, done, err := shared.MigrateFrom(ctx, local, mixerID, a.log)
		switch {
		case err != nil:
			a.log.Warn("spec migration failed; it is retried the next time this Mixer takes the lease", "error", err)
			continue
		case done:
			a.log.Info("specs are served from the shared store; this Mixer's local spec store is not used")
		default:
			a.log.Info("copied the local spec store into the shared one",
				"copied", res.Copied, "skipped", len(res.Conflicts), "skipped_versions", res.Conflicts)
		}
		return
	}
}
