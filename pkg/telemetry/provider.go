package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Init initializes the OpenTelemetry SDKs (Trace, Metric, Log)
// using the NatsExporter.

// MultiHandler fan-out logs to multiple handlers.
type MultiHandler struct {
	handlers []slog.Handler
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
			// Best effort
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

// Global state for dynamic updates (not ideal but practical for this refactor)
var (
	currentShutdown func(context.Context) error
	currentBus      bus.Bus
	currentConfig   Config
	originalHandler slog.Handler // Capture the initial handler to avoid recursion
	initOnce        sync.Once
)

// Init initializes/re-initializes telemetry.
// If already running, it shuts down previous instance first.
func Init(ctx context.Context, cfg Config, b bus.Bus) (func(context.Context) error, error) {
	// 0. Capture original handler ONCE
	initOnce.Do(func() {
		originalHandler = slog.Default().Handler()
	})

	// Shutdown existing if any
	if currentShutdown != nil {
		_ = currentShutdown(context.Background())
	}

	// Update globals
	currentConfig = cfg
	currentBus = b

	// 1. Resource (Metadata about this node)
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"", // Use empty schema URL
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.ServiceInstanceID(fmt.Sprintf("%x", cfg.EntityID)),
			// Also add MachineName as attribute?
			// Standard semconv doesn't have "machine.name" exactly in this context,
			// but we can add custom attribute.
			// attribute.String("machine.name", cfg.EntityName),
			// (Ignoring for now as it's in the exporter payload)
		),
	)
	if err != nil {
		return nil, err
	}

	// 2. Exporters (Split to avoid method collision)
	writer := NewNatsWriter(b, cfg.EntityID, cfg.EntityName, cfg.BaseSubject)
	spanExporter := NewSpanExporter(writer)
	logExporter := NewLogExporter(writer)
	metricExporter := NewMetricExporter(writer)

	// Config: Batch Interval (Default 5s)
	batchInterval := 5 * time.Second
	if cfg.BatchIntervalString != "" {
		if d, err := time.ParseDuration(cfg.BatchIntervalString); err == nil {
			batchInterval = d
		}
	}

	// Config: Batch Size (Default 512)
	batchSize := 512
	if cfg.MaxBatchSize > 0 {
		batchSize = cfg.MaxBatchSize
	}

	// 3. Trace Provider
	// Processors: DualID (Enrichment) -> Batch (Export)
	dualIDProcessor := NewDualIDSpanProcessor()

	batchSpanProcessor := sdktrace.NewBatchSpanProcessor(spanExporter,
		sdktrace.WithBatchTimeout(batchInterval),
		sdktrace.WithMaxExportBatchSize(batchSize),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(dualIDProcessor), // Runs first
		sdktrace.WithSpanProcessor(batchSpanProcessor),
	)
	otel.SetTracerProvider(tp)

	// 4. Log Provider
	batchLogProcessor := sdklog.NewBatchProcessor(logExporter,
		sdklog.WithExportInterval(batchInterval),
	)

	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(batchLogProcessor),
	)

	// 5. Metric Provider
	metricReader := sdkmetric.NewPeriodicReader(metricExporter,
		sdkmetric.WithInterval(batchInterval),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(metricReader),
	)
	otel.SetMeterProvider(mp)

	// 6. Connect Slog -> OTel
	// We replace the global logger to send logs to the OTel Bridge.
	// But we preserve the previous stdout handler if we are re-initializing?
	// Actually, Init is usually called with a fresh start logic.
	// For UpdateIdentity, we might want to be careful not to create nested handlers.

	// Use originalHandler as the base, ensuring we never wrap ourselves recursively.
	currentHandler := originalHandler
	if currentHandler == nil {
		// Fallback if initOnce logic failed for some reason (shouldn't happen)
		currentHandler = slog.Default().Handler()
	}

	otelLogger := otelslog.NewLogger(cfg.ServiceName, otelslog.WithLoggerProvider(lp))
	otelHandler := otelLogger.Handler()

	multiHandler := NewMultiHandler(currentHandler, otelHandler)
	slog.SetDefault(slog.New(multiHandler))

	// 7. Propagators
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Shutdown Function
	cleanup := func(shutdownCtx context.Context) error {
		var errs error
		if err := tp.Shutdown(shutdownCtx); err != nil {
			errs = err
		}
		if err := lp.Shutdown(shutdownCtx); err != nil {
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

// UpdateIdentity restarts telemetry with new ID and Name
func UpdateIdentity(ctx context.Context, id uint64, name string) error {
	if currentBus == nil {
		return fmt.Errorf("telemetry not initialized")
	}
	newConfig := currentConfig
	newConfig.EntityID = id
	newConfig.EntityName = name

	_, err := Init(ctx, newConfig, currentBus)
	return err
}
