package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
)

// NatsWriter implements the io.Writer interface for JSON logs,
// but also provides methods for Spans and Metrics.
type NatsWriter struct {
	bus         bus.Bus
	entityID    uint64
	entityName  string
	baseSubject string
}

func NewNatsWriter(b bus.Bus, entityID uint64, entityName, baseSubject string) *NatsWriter {
	return &NatsWriter{
		bus:         b,
		entityID:    entityID,
		entityName:  entityName,
		baseSubject: baseSubject,
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
	if err := json.Unmarshal(p, &record); err != nil {
		return 0, err
	}
	record["entity_id"] = w.entityID
	record["entity_name"] = w.entityName

	data, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}

	msg := fluxmsg.New()
	msg.Data = map[string]any{"record": json.RawMessage(data)}
	msg.Metadata["type"] = "telemetry.log.json"
	msg.Metadata["scope"] = "telemetry"

	if err := w.bus.Publish(w.baseSubject+".logs.json", msg); err != nil {
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
			"type":        "span",
			"trace_id":    span.SpanContext().TraceID().String(),
			"span_id":     span.SpanContext().SpanID().String(),
			"parent_id":   "",
			"name":        span.Name(),
			"start_time":  span.StartTime().UnixMicro(),
			"end_time":    span.EndTime().UnixMicro(),
			"status":      span.Status().Code.String(),
			"kind":        span.SpanKind().String(),
			"entity_id":   e.entityID,
			"entity_name": e.entityName,
		}
		if span.Parent().IsValid() {
			s["parent_id"] = span.Parent().SpanID().String()
		}
		s["attributes"] = attributeToMap(span.Attributes())
		batchedPayload = append(batchedPayload, s)
	}

	msg := fluxmsg.New()
	msg.Data = map[string]any{"batch": batchedPayload}
	msg.Metadata["type"] = "telemetry.batch.spans"
	msg.Metadata["scope"] = "telemetry"

	return e.bus.Publish(e.baseSubject+".spans", msg)
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
		rec.WalkAttributes(func(kv otellog.KeyValue) bool {
			attrs[string(kv.Key)] = logValueToInterface(kv.Value)
			return true
		})
		r["attributes"] = attrs
		batchedPayload = append(batchedPayload, r)
	}

	msg := fluxmsg.New()
	msg.Data = map[string]any{"batch": batchedPayload}
	msg.Metadata["type"] = "telemetry.batch.logs"
	msg.Metadata["scope"] = "telemetry"

	return e.bus.Publish(e.baseSubject+".logs", msg)
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
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for _, m := range scopeMetrics.Metrics {
			// Simplified Metric Export (Gauge/Sum only for demo)
			// Loop points
			// Convert to simple format
			points := resolvePoints(m)
			for _, p := range points {
				payload := map[string]interface{}{
					"name":        m.Name,
					"description": m.Description,
					"unit":        m.Unit,
					"type":        p.Type,
					"value":       p.Value,
					"timestamp":   p.LinkTime,
					"attributes":  p.Attributes,
					"entity_id":   e.entityID,
					"entity_name": e.entityName,
				}

				// Fix: fluxmsg.NewFromData -> manual wrap
				msg := fluxmsg.New()
				msg.Data = payload
				msg.Metadata["type"] = "telemetry.metric"
				// fluxmsg.Data is map[string]any.
				// Sink handles JSON marshaling if required.

				_ = e.bus.Publish(e.baseSubject+".metrics", msg)
			}
		}
	}
	return nil
}

func (e *MetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

// Point helper structure
type simplePoint struct {
	Type       string
	Value      float64
	LinkTime   time.Time
	Attributes map[string]interface{}
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
				Attributes: attributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Gauge[float64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "gauge",
				Value:      p.Value,
				LinkTime:   p.Time,
				Attributes: attributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Sum[int64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "sum",
				Value:      float64(p.Value),
				LinkTime:   p.Time,
				Attributes: attributeToMap(p.Attributes.ToSlice()),
			})
		}
	case metricdata.Sum[float64]:
		for _, p := range data.DataPoints {
			points = append(points, simplePoint{
				Type:       "sum",
				Value:      p.Value,
				LinkTime:   p.Time,
				Attributes: attributeToMap(p.Attributes.ToSlice()),
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

func (e *MetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *MetricExporter) Shutdown(ctx context.Context) error   { return nil }
func (e *MetricExporter) ForceFlush(ctx context.Context) error { return nil }

func logValueToInterface(v otellog.Value) interface{} {
	switch v.Kind() {
	case otellog.KindBool:
		return v.AsBool()
	case otellog.KindFloat64:
		return v.AsFloat64()
	case otellog.KindInt64:
		return v.AsInt64()
	case otellog.KindString:
		return v.AsString()
	case otellog.KindBytes:
		return v.AsBytes()
	case otellog.KindSlice:
		return v.AsString() // Simplify slice for now
	case otellog.KindMap:
		return v.AsString() // Simplify map for now
	default:
		return v.AsString()
	}
}
