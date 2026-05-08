// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func TestSourceHandler_Forensic(t *testing.T) {
	mock := &mockHandler{}
	h := &SourceHandler{next: mock}

	t.Run("HandleWithSource", func(t *testing.T) {
		record := slog.Record{
			Time:    time.Now(),
			Message: "test message",
			Level:   slog.LevelInfo,
			PC:      0, // PC 0 will skip source logic but exercise the fallback
		}
		_ = h.Handle(context.Background(), record)
		assert.True(t, mock.handled)
	})

	t.Run("PassThroughMethods", func(t *testing.T) {
		assert.Equal(t, mock, h.Next())
		assert.NotNil(t, h.WithAttrs(nil))
		assert.NotNil(t, h.WithGroup("test"))
		assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
	})
}

func TestMultiHandler_Forensic(t *testing.T) {
	h1 := &mockHandler{}
	h2 := &mockHandler{}
	m := NewMultiHandler(h1, h2)

	t.Run("FanOut", func(t *testing.T) {
		_ = m.Handle(context.Background(), slog.Record{Message: "fanout"})
		assert.True(t, h1.handled)
		assert.True(t, h2.handled)
	})

	t.Run("Management", func(t *testing.T) {
		assert.Len(t, m.Handlers(), 2)
		assert.True(t, m.Enabled(context.Background(), slog.LevelInfo))
		assert.NotNil(t, m.WithAttrs(nil))
		assert.NotNil(t, m.WithGroup("g"))
	})
}

func TestExporters_Forensic(t *testing.T) {
	b := bus.NewMockBus()
	gen, _ := idgen.New(uuid.New())
	eid := uuid.New()
	writer := NewNatsWriter(b, eid, "test-node", "flux.telemetry.test", gen, 100*time.Millisecond)

	t.Run("NatsWriter_JSONLog", func(t *testing.T) {
		logData := map[string]interface{}{"msg": "test"}
		raw, _ := json.Marshal(logData)
		n, err := writer.Write(raw)
		assert.NoError(t, err)
		assert.Equal(t, len(raw), n)
	})

	t.Run("SpanExporter", func(t *testing.T) {
		exp := NewSpanExporter(writer)
		err := exp.ExportSpans(context.Background(), []trace.ReadOnlySpan{
			&mockSpan{name: "test-span"},
		})
		assert.NoError(t, err)
	})

	t.Run("MetricExporter", func(t *testing.T) {
		exp := NewMetricExporter(writer)
		err := exp.Export(context.Background(), &metricdata.ResourceMetrics{
			ScopeMetrics: []metricdata.ScopeMetrics{
				{
					Metrics: []metricdata.Metrics{
						{
							Name: "test.metric",
							Data: metricdata.Gauge[int64]{
								DataPoints: []metricdata.DataPoint[int64]{
									{Value: 10, Time: time.Now()},
								},
							},
						},
					},
				},
			},
		})
		assert.NoError(t, err)
	})
}

// ------ Mocks ------

type mockHandler struct {
	handled bool
}

func (m *mockHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (m *mockHandler) Handle(context.Context, slog.Record) error { m.handled = true; return nil }
func (m *mockHandler) WithAttrs([]slog.Attr) slog.Handler        { return m }
func (m *mockHandler) WithGroup(string) slog.Handler             { return m }

type mockSpan struct {
	trace.ReadOnlySpan
	name string
}

func (s *mockSpan) Name() string                       { return s.name }
func (s *mockSpan) SpanContext() oteltrace.SpanContext { return oteltrace.SpanContext{} }
func (s *mockSpan) Parent() oteltrace.SpanContext      { return oteltrace.SpanContext{} }
func (s *mockSpan) StartTime() time.Time               { return time.Now() }
func (s *mockSpan) EndTime() time.Time                 { return time.Now() }
func (s *mockSpan) Status() trace.Status               { return trace.Status{} }
func (s *mockSpan) SpanKind() oteltrace.SpanKind       { return oteltrace.SpanKindInternal }
func (s *mockSpan) Attributes() []attribute.KeyValue   { return nil }
func (s *mockSpan) Resource() *sdkresource.Resource    { return nil }
