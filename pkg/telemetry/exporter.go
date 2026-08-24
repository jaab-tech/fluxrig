// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

// NatsWriter implements the io.Writer interface for JSON logs,
// but also provides methods for Spans and Metrics.
type NatsWriter struct {
	bus         bus.Bus
	entityID    uuid.UUID
	entityName  string
	baseSubject string
	gen         *idgen.IDGenerator
	timeout     time.Duration
}

func NewNatsWriter(b bus.Bus, entityID uuid.UUID, entityName, baseSubject string, gen *idgen.IDGenerator, timeout time.Duration) *NatsWriter {
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	return &NatsWriter{
		bus:         b,
		entityID:    entityID,
		entityName:  entityName,
		baseSubject: baseSubject,
		gen:         gen,
		timeout:     timeout,
	}
}

// Write implements io.Writer for Logs (OTel Logs Exporter writes JSON bytes here)
func (w *NatsWriter) Write(p []byte) (n int, err error) {
	// OTel JSON Log Record -> Wrap in FluxMsg
	// Inject common metadata (machine_id, machine_name)
	// Since input is bytes, wrap in structure if needed
	// or assume the bytes are the 'body'.
	//
	// However, the OTel JSON exporter produces a complete JSON object.
	// We'll wrap that in map to add extra context or just send it as data.
	// Let's add context at Ingest time or Client time?
	// Client time is better.
	//
	// Alternative: Unmarshal to map, append fields, and Marshal, or use a structured transport wrapper.
	// Performance penalty, but safer.
	var record map[string]interface{}
	if errJSON := json.Unmarshal(p, &record); errJSON != nil {
		return 0, errJSON
	}
	record["entity_id"] = w.entityID
	record["entity_name"] = w.entityName

	msg := fluxmsg.New()
	msg.FluxID, _ = w.gen.NextFluxID()
	msg.Data = map[string]any{"record": record}
	msg.Metadata["type"] = "telemetry.log.json"
	msg.Metadata["scope"] = "telemetry"

	entityName := w.entityName
	if entityName == "" {
		entityName = "unknown"
	}

	if err := w.bus.Publish(context.Background(), w.baseSubject+"."+entityName+".logs.json", msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ------ Trace Exporter ------

type SpanExporter struct {
	*NatsWriter
}

func NewSpanExporter(w *NatsWriter) *SpanExporter {
	return &SpanExporter{w}
}

func (e *SpanExporter) ExportSpans(ctx context.Context, spans []trace.ReadOnlySpan) error {
	batchedPayload := make([]map[string]interface{}, 0, len(spans))
	for _, span := range spans {
		s := map[string]interface{}{
			"type":           "span",
			"trace_id":       span.SpanContext().TraceID().String(),
			"span_id":        span.SpanContext().SpanID().String(),
			"parent_span_id": "",
			"name":           span.Name(),
			"start_time":     span.StartTime().UnixMicro(),
			"end_time":       span.EndTime().UnixMicro(),
			"status":         span.Status().Code.String(),
			"kind":           span.SpanKind().String(),
			"entity_id":      e.entityID,
			"entity_name":    e.entityName,
		}
		if span.Parent().IsValid() {
			s["parent_span_id"] = span.Parent().SpanID().String()
			slog.Debug("SpanExporter: Exporting linked span", "name", span.Name(), "span_id", s["span_id"], "parent_span_id", s["parent_span_id"])
		} else {
			slog.Debug("SpanExporter: Exporting ROOT span (no parent)", "name", span.Name(), "span_id", s["span_id"])
		}
		s["attributes"] = attributeToMap(span.Attributes())
		batchedPayload = append(batchedPayload, s)
	}

	msg := fluxmsg.New()
	msg.FluxID, _ = e.gen.NextFluxID()
	msg.Data = map[string]any{"batch": batchedPayload}
	msg.Metadata["type"] = "telemetry.batch.spans"
	msg.Metadata["scope"] = "telemetry"

	// QoS: Strict timeout for telemetry
	exportCtx, cancel := context.WithTimeout(ctx, e.timeout)
	entityName := e.entityName
	if entityName == "" {
		entityName = "unknown"
	}
	err := e.bus.Publish(exportCtx, e.baseSubject+"."+entityName+".spans", msg)
	cancel()
	if err == nil {
		slog.Debug("Telemetry: Spans exported successfully", "count", len(spans), "subject", e.baseSubject+"."+entityName+".spans")
	}
	return err
}

func (e *SpanExporter) Shutdown(ctx context.Context) error { return nil }

// ------ Log Exporter ------

type LogExporter struct {
	*NatsWriter
}

func NewLogExporter(w *NatsWriter) *LogExporter {
	return &LogExporter{w}
}

func (e *LogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	batchedPayload := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		r := map[string]interface{}{
			"type":        "log",
			"timestamp":   rec.Timestamp().UnixMicro(),
			"severity":    rec.Severity().String(),
			"body":        rec.Body().AsString(),
			"entity_id":   e.entityID,
			"entity_name": e.entityName,
		}
		if rec.TraceID().IsValid() {
			r["trace_id"] = rec.TraceID().String()
		}
		if rec.SpanID().IsValid() {
			r["span_id"] = rec.SpanID().String()
		}
		attrs := make(map[string]interface{})
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			attrs[string(kv.Key)] = logValueToInterface(kv.Value)
			return true
		})
		r["attributes"] = attrs
		batchedPayload = append(batchedPayload, r)
	}

	msg := fluxmsg.New()
	msg.FluxID, _ = e.gen.NextFluxID()
	msg.Data = map[string]any{"batch": batchedPayload}
	msg.Metadata["type"] = "telemetry.batch.logs"
	msg.Metadata["scope"] = "telemetry"

	// QoS: Strict timeout for telemetry
	exportCtx, cancel := context.WithTimeout(ctx, e.timeout)
	entityName := e.entityName
	if entityName == "" {
		entityName = "unknown"
	}
	err := e.bus.Publish(exportCtx, e.baseSubject+"."+entityName+".logs", msg)
	cancel()
	return err
}

func (e *LogExporter) Shutdown(ctx context.Context) error   { return nil }
func (e *LogExporter) ForceFlush(ctx context.Context) error { return nil }

// ------ Metric Exporter ------

type MetricExporter struct {
	*NatsWriter
}

func NewMetricExporter(w *NatsWriter) *MetricExporter {
	return &MetricExporter{w}
}

func (e *MetricExporter) Export(ctx context.Context, metrics *metricdata.ResourceMetrics) error {
	slog.Debug("MetricExporter: Export called", "scope_metrics_count", len(metrics.ScopeMetrics))
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for _, m := range scopeMetrics.Metrics {
			points := resolvePoints(m)
			resAttrs := metrics.Resource.Attributes()
			idAttrs := make(map[string]interface{})
			for _, kv := range resAttrs {
				k := string(kv.Key)
				if isAllowedMetricAttribute(k) {
					idAttrs[k] = kv.Value.AsInterface()
				}
			}

			for _, p := range points {
				// 1. Merge resource attributes as defaults (don't overwrite data point attributes)
				for k, v := range idAttrs {
					if _, exists := p.Attributes[k]; !exists {
						p.Attributes[k] = v
					}
				}

				// 2. Resolve Dynamic Identity (from attributes)
				finalID := e.entityID
				if idVal, ok := p.Attributes["flux.id"].(string); ok {
					if uid, err := uuid.Parse(idVal); err == nil {
						finalID = uid
					}
				}

				finalName := e.entityName
				if nameVal, ok := p.Attributes["flux.name"].(string); ok {
					finalName = nameVal
				}

				payload := map[string]interface{}{
					"timestamp":   p.LinkTime.UnixMicro(),
					"attributes":  p.Attributes,
					"entity_id":   finalID,
					"entity_name": finalName,
					"name":        m.Name + p.Suffix,
					"value":       p.Value,
				}

				msg := fluxmsg.New()
				msg.FluxID, _ = e.gen.NextFluxID()
				msg.Data = payload
				msg.Metadata["type"] = "telemetry.metric"
				msg.Metadata["metric.name"] = m.Name + p.Suffix

				exportCtx, cancel := context.WithTimeout(ctx, e.timeout)
				entityName := e.entityName
				if entityName == "" {
					entityName = "unknown"
				}
				_ = e.bus.Publish(exportCtx, e.baseSubject+"."+entityName+".metrics", msg)
				cancel()

			}
		}
	}
	return nil
}

func (e *MetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	switch k {
	case sdkmetric.InstrumentKindCounter,
		sdkmetric.InstrumentKindHistogram,
		sdkmetric.InstrumentKindObservableCounter:
		// These should be aggregated as deltas to simplify DuckDB logic
		return metricdata.DeltaTemporality
	case sdkmetric.InstrumentKindGauge,
		sdkmetric.InstrumentKindObservableGauge,
		sdkmetric.InstrumentKindUpDownCounter,
		sdkmetric.InstrumentKindObservableUpDownCounter:
		// These represent absolute point-in-time values or balances.
		// Delta here would represent a "change in balance" which is confusing for our charts.
		return metricdata.CumulativeTemporality
	default:
		return metricdata.DeltaTemporality
	}
}

// Point helper structure
type simplePoint struct {
	Type       string
	Value      float64
	LinkTime   time.Time
	Attributes map[string]interface{}
	Suffix     string
}

func resolvePoints(m metricdata.Metrics) []simplePoint {
	var points []simplePoint

	switch data := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "gauge",
				Value:      float64(p.Value),
				LinkTime:   p.Time,
				Attributes: metricAttributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Gauge[float64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "gauge",
				Value:      p.Value,
				LinkTime:   p.Time,
				Attributes: metricAttributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Sum[int64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "sum",
				Value:      float64(p.Value),
				LinkTime:   p.Time,
				Attributes: metricAttributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Sum[float64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "sum",
				Value:      p.Value,
				LinkTime:   p.Time,
				Attributes: metricAttributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Histogram[float64]:
		for _, p := range data.DataPoints {
			attrs := metricAttributeToMap(p.Attributes.ToSlice())
			// Export Sum
			points = append(points, simplePoint{
				Type:       "histogram_sum",
				Value:      p.Sum,
				LinkTime:   p.Time,
				Attributes: attrs,
				Suffix:     ".sum",
			})
			// Export Count
			points = append(points, simplePoint{
				Type:       "histogram_count",
				Value:      float64(p.Count),
				LinkTime:   p.Time,
				Attributes: attrs,
				Suffix:     ".count",
			})
		}
	}
	return points
}

// attributeToMap converts OTel attributes to map
func attributeToMap(attrs []attribute.KeyValue) map[string]interface{} {
	m := make(map[string]interface{}, len(attrs))
	for _, kv := range attrs {
		m[string(kv.Key)] = kv.Value.AsInterface()
	}
	return m
}

// metricAttributeToMap filters attributes to prevent cardinality explosion
func metricAttributeToMap(attrs []attribute.KeyValue) map[string]interface{} {
	m := make(map[string]interface{})
	for _, kv := range attrs {
		k := string(kv.Key)
		if isAllowedMetricAttribute(k) {
			m[k] = kv.Value.AsInterface()
		}
	}
	return m
}

func isAllowedMetricAttribute(k string) bool {
	switch k {
	case "component", "subject", "error", "status", "gear_id", "port_id", "outcome", "handler":
		return true
	case "state", "cpu", "device", "disk", "usage", "direction", "process", "filesystem":
		return true
	}
	return strings.HasPrefix(k, "flux.")
}

func (e *MetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *MetricExporter) Shutdown(ctx context.Context) error   { return nil }
func (e *MetricExporter) ForceFlush(ctx context.Context) error { return nil }

// logValueToInterface converts an OTel attribute value into the plain Go value
// that goes onto the bus.
//
// The log signal used to carry its own value type; since otel/log v0.21.0 it
// shares attribute.Value with traces and metrics, and that type already knows
// how to unwrap itself. The switch this replaces mapped each kind by hand and
// flattened slices and maps to their string form, which lost their contents.
func logValueToInterface(v attribute.Value) interface{} {
	return v.AsInterface()
}
