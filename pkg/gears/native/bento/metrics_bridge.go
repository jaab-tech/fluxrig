package bento

import (
	"context"
	"log/slog"

	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/warpstreamlabs/bento/public/service"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Bridge implements service.MetricsExporter to pipe Bento metrics to OTel.
type Bridge struct {
	meter metric.Meter
}

// NewBridgeCtor returns a constructor for the bridge.
func NewBridgeCtor() service.MetricsExporterConstructor {
	return func(conf *service.ParsedConfig, log *service.Logger) (service.MetricsExporter, error) {
		return &Bridge{
			meter: telemetry.GetMeter("bento.internal"),
		}, nil
	}
}

// Close implements service.MetricsExporter.
func (b *Bridge) Close(ctx context.Context) error {
	return nil
}

// NewCounterCtor implements service.MetricsExporter.
func (b *Bridge) NewCounterCtor(name string, labelKeys ...string) service.MetricsExporterCounterCtor {
	return func(labels ...string) service.MetricsExporterCounter {
		cnt, err := b.meter.Int64Counter("bento." + name)
		if err != nil {
			slog.Error("failed to create bento counter", "name", name, "error", err)
			return &noopCounter{}
		}
		return &otelCounter{
			inst:   cnt,
			labels: makeAttrs(labelKeys, labels),
		}
	}
}

// NewGaugeCtor implements service.MetricsExporter.
func (b *Bridge) NewGaugeCtor(name string, labelKeys ...string) service.MetricsExporterGaugeCtor {
	return func(labels ...string) service.MetricsExporterGauge {
		gauge, err := b.meter.Int64UpDownCounter("bento." + name)
		if err != nil {
			slog.Error("failed to create bento gauge", "name", name, "error", err)
			return &noopGauge{}
		}
		return &otelGauge{
			inst:   gauge,
			labels: makeAttrs(labelKeys, labels),
		}
	}
}

// NewTimerCtor implements service.MetricsExporter.
func (b *Bridge) NewTimerCtor(name string, labelKeys ...string) service.MetricsExporterTimerCtor {
	return func(labels ...string) service.MetricsExporterTimer {
		hist, err := b.meter.Float64Histogram("bento."+name, metric.WithUnit("ns"))
		if err != nil {
			slog.Error("failed to create bento timer", "name", name, "error", err)
			return &noopTimer{}
		}
		return &otelTimer{
			inst:   hist,
			labels: makeAttrs(labelKeys, labels),
		}
	}
}

// --- OTel Wrappers ---

type otelCounter struct {
	inst   metric.Int64Counter
	labels []attribute.KeyValue
}

func (o *otelCounter) Incr(delta int64) {
	o.inst.Add(context.Background(), delta, metric.WithAttributes(o.labels...))
}

type otelGauge struct {
	inst   metric.Int64UpDownCounter
	labels []attribute.KeyValue
	val    int64
}

func (o *otelGauge) Set(value int64) {
	delta := value - o.val
	o.inst.Add(context.Background(), delta, metric.WithAttributes(o.labels...))
	o.val = value
}

type otelTimer struct {
	inst   metric.Float64Histogram
	labels []attribute.KeyValue
}

func (o *otelTimer) Timing(deltaNs int64) {
	o.inst.Record(context.Background(), float64(deltaNs), metric.WithAttributes(o.labels...))
}

// --- Helpers ---

func makeAttrs(keys, values []string) []attribute.KeyValue {
	if len(keys) != len(values) {
		return nil
	}
	attrs := make([]attribute.KeyValue, len(keys))
	for i, k := range keys {
		attrs[i] = attribute.String(k, values[i])
	}
	return attrs
}

// --- NoOps ---
type noopCounter struct{}

func (n *noopCounter) Incr(d int64) {}

type noopGauge struct{}

func (n *noopGauge) Set(v int64) {}

type noopTimer struct{}

func (n *noopTimer) Timing(d int64) {}

// RegisterMetrics registers this bridge with the service.Environment
func RegisterMetrics(env *service.Environment) error {
	// We don't need any config for this bridge, so empty spec.
	return service.RegisterMetricsExporter("fluxrig_otel", service.NewConfigSpec(), NewBridgeCtor())
}
