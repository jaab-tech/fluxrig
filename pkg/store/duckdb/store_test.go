// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"log/slog"
	"testing"
)

func TestStore_Lifecycle(t *testing.T) {
	// 1. NewStore (In-Memory)
	logger := slog.Default()
	s, err := NewStore(logger, "")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.DB() == nil {
		t.Error("DB() returned nil")
	}

	ctx := context.Background()

	// 2. Initialize Schema
	if errMig := s.Migrate(ctx); errMig != nil {
		t.Fatalf("Migrate failed: %v", errMig)
	}

	// Verify table exists
	var tableName string
	err = s.DB().QueryRowContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_name = 'registry'").Scan(&tableName)
	if err != nil {
		t.Errorf("Failed to query schema or table not found: %v", err)
	}
	if tableName != "registry" {
		t.Errorf("Expected table 'registry', got '%s'", tableName)
	}

}
