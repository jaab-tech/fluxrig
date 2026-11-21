// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// MeterConfig configures a process's own metrics, exported through a
// NatsWriter to the telemetry stream: the path a Rack's metrics take to the
// Mixer's sink.
type MeterConfig struct {
	// Writer publishes each exported point on the telemetry subject.
	Writer *NatsWriter
	// ServiceName and ServiceVersion identify the process.
	ServiceName    string
	ServiceVersion string
	// EntityID, EntityName and Component identify the entity the points
	// belong to, as on a Rack's metrics.
	EntityID   uuid.UUID
	EntityName string
	Component  string
	// Interval is how often the points are exported. It must be positive.
	Interval time.Duration
}

// InitMeterProvider starts a meter provider whose points go to cfg.Writer
// every cfg.Interval, and makes it the process's provider (GetMeter). It is
// for a process that does not run Init, which also takes over logging: the
// Mixer. The caller shuts the provider down, which exports what is left.
func InitMeterProvider(cfg MeterConfig) (*sdkmetric.MeterProvider, error) {
	if cfg.Writer == nil {
		return nil, fmt.Errorf("telemetry: meter provider: no writer")
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("telemetry: meter provider: the export interval must be positive, got %s", cfg.Interval)
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.ServiceInstanceID(cfg.EntityID.String()),
			attribute.String("flux.id", cfg.EntityID.String()),
			attribute.String("flux.name", cfg.EntityName),
			attribute.String("flux.type", cfg.Component),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: meter provider: resource: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(NewMetricExporter(cfg.Writer),
			sdkmetric.WithInterval(cfg.Interval))),
	)
	otel.SetMeterProvider(mp)
	currentMP = mp
	return mp, nil
}

// ShutdownMeterProvider exports what is left and stops mp, within timeout.
func ShutdownMeterProvider(mp *sdkmetric.MeterProvider, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if currentMP == mp {
		currentMP = nil
	}
	return mp.Shutdown(ctx)
}
