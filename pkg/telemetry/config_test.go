// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry_test

import (
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

func TestInit_ConfigParsing(t *testing.T) {
	// Reset global state to avoid interference from other tests
	telemetry.ResetGlobalsForTest()

	mockBus := bus.NewMockBus()

	// Case 1: Custom BatchInterval
	cfg := telemetry.Config{
		ServiceName:         "test-service",
		EntityID:            12345,
		BaseSubject:         "test.telemetry",
		BatchIntervalString: "100ms",
		MaxBatchSize:        10,
	}

	shutdown, err := telemetry.Init(context.Background(), cfg, mockBus, nil, nil)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	// We can't easily inspect internal state without reflection or exporting vars,
	// but successful execution covers the parsing blocks.
}

func TestInit_InvalidDuration(t *testing.T) {
	telemetry.ResetGlobalsForTest()
	mockBus := bus.NewMockBus()

	// Case 2: Invalid Duration (should fallback to default, not panic or error)
	cfg := telemetry.Config{
		ServiceName:         "test-service",
		EntityID:            12345,
		EntityName:          "test-machine-name",
		BatchIntervalString: "invalid-duration",
	}

	shutdown, err := telemetry.Init(context.Background(), cfg, mockBus, nil, nil)
	if err != nil {
		t.Fatalf("Init failed even with invalid duration (should be robust): %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
}
