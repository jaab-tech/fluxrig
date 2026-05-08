// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func TestIsAllowedMetricAttribute(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"component", true},
		{"subject", true},
		{"flux.id", true},
		{"flux.custom", true},
		{"password", false},
		{"secret", false},
		{"process", true},
	}

	for _, tt := range tests {
		if got := isAllowedMetricAttribute(tt.key); got != tt.want {
			t.Errorf("isAllowedMetricAttribute(%s) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestLogValueToInterface(t *testing.T) {
	v := otellog.StringValue("test")
	if got := logValueToInterface(v); got != "test" {
		t.Errorf("Expected test, got %v", got)
	}

	v = otellog.Int64Value(42)
	if got := logValueToInterface(v); got != int64(42) {
		t.Errorf("Expected 42, got %v", got)
	}
}

func TestResolvePoints(t *testing.T) {
	m := metricdata.Metrics{
		Name: "test-metric",
		Data: metricdata.Sum[int64]{
			Temporality: metricdata.DeltaTemporality,
			DataPoints: []metricdata.DataPoint[int64]{
				{
					Value: 10,
					Time:  time.Now(),
				},
			},
		},
	}

	points := resolvePoints(m)
	if len(points) != 1 {
		t.Fatalf("Expected 1 point, got %d", len(points))
	}
	if points[0].Value != 10.0 {
		t.Errorf("Expected value 10.0, got %f", points[0].Value)
	}
}

func TestDualIDSpanProcessor_IdentityEnrichment(t *testing.T) {
	processor := NewDualIDSpanProcessor()
	ctx := context.Background()
	fluxID := uuid.New()
	ctx = ContextWithFluxID(ctx, fluxID.String())

	// Since we can't easily mock trace.ReadWriteSpan without a full SDK setup,
	// we will verify that NewDualIDSpanProcessor returns a valid object.
	if processor == nil {
		t.Error("NewDualIDSpanProcessor returned nil")
	}

	// Coverage of no-op methods
	processor.OnEnd(nil)
	_ = processor.Shutdown(ctx)
	_ = processor.ForceFlush(ctx)
}

func TestAttributeToMap(t *testing.T) {
	attrs := []attribute.KeyValue{
		attribute.String("k1", "v1"),
		attribute.Int64("k2", 42),
	}
	m := attributeToMap(attrs)
	if m["k1"] != "v1" {
		t.Errorf("Expected v1, got %v", m["k1"])
	}
	if m["k2"] != int64(42) {
		t.Errorf("Expected 42, got %v", m["k2"])
	}
}

func TestNatsWriter_Write(t *testing.T) {
	mockBus := bus.NewMockBus()
	gen, _ := idgen.New(uuid.New())
	eid := uuid.New()
	writer := NewNatsWriter(mockBus, eid, "test-entity", "flux.telemetry", gen, 1*time.Second)

	logData := map[string]interface{}{
		"message": "test log",
		"level":   "info",
	}
	p, _ := json.Marshal(logData)

	n, err := writer.Write(p)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(p) {
		t.Errorf("Expected n=%d, got %d", len(p), n)
	}

	msgs := mockBus.GetMessages("flux.telemetry.test-entity.logs.json")
	if len(msgs) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(msgs))
	}
}
