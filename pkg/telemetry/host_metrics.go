package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
)

// InitHostMetrics starts collection of Host and Runtime metrics.
func InitHostMetrics(ctx context.Context, cfg MetricsConfig) error {
	if cfg.HostEnabled {
		// host.Start() initializes host metrics collection.
		// It uses the global MeterProvider.
		if err := host.Start(); err != nil {
			return fmt.Errorf("failed to start host metrics: %w", err)
		}
	}

	if cfg.RuntimeEnabled {
		// runtime.Start() initializes Go runtime metrics.
		// It uses the global MeterProvider.
		if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
			return fmt.Errorf("failed to start runtime metrics: %w", err)
		}
	}
	return nil
}
