// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	loggerPkg "github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/logger/rotator"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/shipper"
	"github.com/jaab-tech/fluxrig/pkg/telemetry/wal"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// SourceHandler overrides to handle relative paths
type SourceHandler struct {
	next slog.Handler
}

// ... SourceHandler methods ...
func (h *SourceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *SourceHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		if f.File != "" {
			file := f.File
			if idx := strings.Index(file, "fluxrig/"); idx != -1 {
				file = file[idx+len("fluxrig/"):]
			}
			funcName := f.Function
			if idx := strings.LastIndex(funcName, "/"); idx != -1 {
				funcName = funcName[idx+1:]
			}
			if idx := strings.Index(funcName, "."); idx != -1 {
				funcName = funcName[idx+1:]
			}

			// Modify record ATTRS to include source
			// Assume underlying handler supports WithAttrs if needed
			// For simplicity with slog, add to record logic
			r = r.Clone()
			r.AddAttrs(
				slog.String("code.file.path", file),
				slog.Int("code.line.number", f.Line),
				slog.String("code.function.name", funcName),
			)
			return h.next.Handle(ctx, r)
		}
	}
	return h.next.Handle(ctx, r)
}

func (h *SourceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SourceHandler{next: h.next.WithAttrs(attrs)}
}

// Next returns the underlying handler.
func (h *SourceHandler) Next() slog.Handler {
	return h.next
}

func (h *SourceHandler) WithGroup(name string) slog.Handler {
	return &SourceHandler{next: h.next.WithGroup(name)}
}

// MultiHandler fan-out logs to multiple handlers.
type MultiHandler struct {
	handlers []slog.Handler
}

// Handlers returns the list of wrapped handlers.
func (m *MultiHandler) Handlers() []slog.Handler {
	return m.handlers
}

func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r)
		}
	}
	return nil
}

func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return NewMultiHandler(handlers...)
}

func (m *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return NewMultiHandler(handlers...)
}

// Global state
var (
	currentShutdown func(context.Context) error
	currentMetrics  *Metrics
	currentMP       *sdkmetric.MeterProvider
)

// GetMetrics returns the initialized OTel instruments.
func GetMetrics() *Metrics {
	return currentMetrics
}

// GetMeter returns a named meter from the active provider.
func GetMeter(name string) metric.Meter {
	if currentMP != nil {
		return currentMP.Meter(name)
	}
	return otel.GetMeterProvider().Meter(name)
}

// Init initializes telemetry with Binary WAL + Shipper strategy.
func Init(ctx context.Context, cfg Config, b bus.Bus, logBuffer *BufferHandler, gen *idgen.IDGenerator) (func(context.Context) error, error) {
	if currentShutdown != nil {
		_ = currentShutdown(context.Background())
	}

	if cfg.MaxBatchSize == 0 {
		cfg.MaxBatchSize = 512
	}

	// 1. Resource (OTel)
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.ServiceInstanceID(fmt.Sprintf("%x", cfg.EntityID)),
			//nolint:gosec // conversion safe for entity IDs
			attribute.Int64("flux.id", int64(cfg.EntityID)),
			attribute.String("flux.name", cfg.EntityName),
		),
	)
	if err != nil {
		return nil, err
	}

	// 2. WAL Setup (Mandatory)
	// WAL location is inside Store Dir
	storeDir := cfg.Store.Dir
	if storeDir == "" {
		storeDir = "./data"
	}
	walDir := filepath.Join(storeDir, "wal")

	if errMkdir := os.MkdirAll(walDir, 0750); errMkdir != nil {
		return nil, fmt.Errorf("failed to create wal dir %s: %w", walDir, errMkdir)
	}

	// [WAL Configuration]
	walOpts := &wal.Options{
		NoSync: true,
	}

	walInstance, err := wal.Open(walDir, walOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL %s: %w", walDir, err)
	}

	// Parse Level (Global)
	globalLevel := loggerPkg.ParseLevel(cfg.Logging.Level)

	// WAL Level matches Global
	binLevel := globalLevel

	// WAL Handler
	walHandler := wal.NewHandler(walInstance, gen, cfg.EntityID, cfg.EntityName, cfg.Component, &slog.HandlerOptions{
		Level: binLevel,
	})

	// 3. Text Logs (Using root LoggingConfig)
	var handlers []slog.Handler
	handlers = append(handlers, walHandler) // Always log to WAL

	// If Filename is set, we enable text logging (implicitly enabled if configured?)
	// Or we assume it is always enabled unless suppressed?
	// Legacy config had 'Enabled'. New config has 'Filename'.
	// If Filename is empty is it disabled? Defaults say "rack.log".
	// Let's assume always enabled for now.

	textFilename := cfg.Logging.Filename
	if textFilename == "" {
		textFilename = "rack.log"
	}

	// Text Log Path
	textPath := textFilename

	// Ensure directory exists
	if logDir := filepath.Dir(textPath); logDir != "." && logDir != "/" {
		if errMkdir := os.MkdirAll(logDir, 0750); errMkdir != nil {
			return nil, fmt.Errorf("failed to create log dir %s: %w", logDir, errMkdir)
		}
	}

	textRotator, err := rotator.New(textPath, cfg.Logging.MaxSizeMB, cfg.Logging.MaxBackups, cfg.Logging.Compress)
	if err != nil {
		return nil, fmt.Errorf("failed to init Text rotator: %w", err)
	}

	// Use FluxHandler
	l := loggerPkg.New(loggerPkg.Config{
		Level:      cfg.Logging.Level,
		EntityType: loggerPkg.EntityType(cfg.Component),
		Name:       cfg.EntityName,
		Writer:     textRotator,
	})
	handlers = append(handlers, l.Handler())

	// 3b. Stdout Logs (Compliant Format)
	if cfg.StdoutEnabled {
		level := cfg.StdoutLevel
		if level == "" {
			level = "info"
		}
		stdoutHandler := loggerPkg.New(loggerPkg.Config{
			Level:      level,
			EntityType: loggerPkg.EntityType(cfg.Component),
			Name:       cfg.EntityName,
			Writer:     os.Stdout,
		}).Handler()
		handlers = append(handlers, stdoutHandler)
	}

	// Combine Types
	multi := NewMultiHandler(handlers...)
	sourceWrapped := &SourceHandler{next: multi}
	slog.SetDefault(slog.New(sourceWrapped))

	// 4. Start Log Shipper
	// Cursor Path (Store Dir)
	cursorPath := filepath.Join(storeDir, "telemetry_cursor.json")
	cursor, err := shipper.NewCursor(cursorPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load cursor: %w", err)
	}

	// Log Shipper
	// Using configured Throttling + MaxWALSize from Store
	logShipper := shipper.NewLogShipper(b, walInstance, cursor, cfg.BaseSubject, cfg.Store.WALMaxSizeMB, cfg.Throttling.Rate, cfg.Throttling.Burst)
	logShipper.Start()

	// 6. Traces & Metrics (Standard OTel via NATS)
	// 6. Traces & Metrics (Standard OTel via NATS)
	qosTimeout, _ := time.ParseDuration(cfg.QoSPublishTimeout)
	if qosTimeout == 0 {
		qosTimeout = 500 * time.Millisecond
	}
	writer := NewNatsWriter(b, cfg.EntityID, cfg.EntityName, cfg.BaseSubject, gen, qosTimeout)
	spanExporter := NewSpanExporter(writer)
	metricExporter := NewMetricExporter(writer)

	// ... OTel boilerplate ...
	batchInterval, _ := time.ParseDuration(cfg.BatchIntervalString)
	if batchInterval == 0 {
		batchInterval = 5 * time.Second // Final fallback if config missing
	}

	dualIDProcessor := NewDualIDSpanProcessor()
	batchSpanProcessor := sdktrace.NewBatchSpanProcessor(spanExporter,
		sdktrace.WithBatchTimeout(batchInterval),
		sdktrace.WithMaxExportBatchSize(cfg.MaxBatchSize),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(dualIDProcessor),
		sdktrace.WithSpanProcessor(batchSpanProcessor),
	)
	otel.SetTracerProvider(tp)

	metricReader := sdkmetric.NewPeriodicReader(metricExporter,
		sdkmetric.WithInterval(batchInterval),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(metricReader),
	)
	otel.SetMeterProvider(mp)
	currentMP = mp

	// Propagators
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// 7. Initialize Instruments (Metrics Struct)
	ms, err := NewMetrics(mp.Meter("fluxrig-telemetry"))
	if err != nil {
		// Log but don't fail entire init? Or fail?
		// Fail implies no metrics.
		// Let's return error.
		return nil, fmt.Errorf("failed to create instruments: %w", err)
	}
	currentMetrics = ms

	// 8. Initialize Host/Runtime Metrics (if enabled)
	slog.Info("Initializing Host/Runtime Metrics", "host", cfg.Metrics.HostEnabled, "runtime", cfg.Metrics.RuntimeEnabled, "bento", cfg.Metrics.BentoEnabled)
	if err := InitHostMetrics(ctx, cfg.Metrics); err != nil {
		slog.Debug("Failed to initialize host/runtime metrics", "error", err)
		// Don't fail the whole startup, just log to debug
	}

	// 8. Flush Buffer (Replay early logs)
	if logBuffer != nil {
		_ = logBuffer.FlushTo(ctx, sourceWrapped)
	}

	// Cleanup
	cleanup := func(shutdownCtx context.Context) error {
		var errs error
		// Stop Shipper (Ensures final logs are sent/cursor saved??)
		// Wait. Shipper sends logs FROM WAL.
		// If application stops, WALWriter closes.
		// Logs in WAL are safe.
		// Shipper sends what it can.
		// Prepare for shutdown
		// Prepare for shutdown
		logShipper.Stop()
		_ = walInstance.Close()

		if err := tp.ForceFlush(shutdownCtx); err != nil {
			errs = err
		}
		if err := tp.Shutdown(shutdownCtx); err != nil {
			errs = err
		}
		if err := mp.ForceFlush(shutdownCtx); err != nil {
			errs = err
		}
		if err := mp.Shutdown(shutdownCtx); err != nil {
			errs = err
		}
		return errs
	}

	currentShutdown = cleanup
	return cleanup, nil
}

// VerifyConnectivity performs a mandatory handshake with the telemetry bus.
// It publishes sync probes relentlessly and waits for loopback.
func VerifyConnectivity(ctx context.Context, b bus.Bus, nodeName string, handshakeTimeout, handshakeInterval time.Duration) error {
	subject := fmt.Sprintf("flux.telemetry.%s.logs", nodeName)

	slog.Info("Waiting for telemetry-plane convergence", "subject", subject)

	hotCh := make(chan struct{})
	sub, err := b.Subscribe(subject, func(_ context.Context, msg *fluxmsg.FluxMsg) {
		if msg.Flags&fluxmsg.FlagSyncProbe != 0 {
			select {
			case <-hotCh:
				// already closed
			default:
				close(hotCh)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to telemetry sync on %s: %w", subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// 2. Relentless Probe Loop
	ticker := time.NewTicker(handshakeInterval)
	defer ticker.Stop()

	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()

	emitProbe := func() {
		probe := fluxmsg.New()
		probe.Flags |= fluxmsg.FlagSyncProbe
		if err := b.Publish(ctx, subject, probe); err != nil {
			slog.Warn("Failed to emit telemetry probe", "subject", subject, "error", err)
		}
	}

	// Initial emission
	emitProbe()

	for {
		select {
		case <-hotCh:
			slog.Info("telemetry-plane convergence confirmed")
			return nil
		case <-ticker.C:
			emitProbe()
		case <-timeout.C:
			return fmt.Errorf("timeout waiting for telemetry convergence on %s", subject)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
