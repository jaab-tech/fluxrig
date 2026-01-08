// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	// 3. Wipe
	if errWipe := s.Wipe(ctx); errWipe != nil {
		t.Errorf("Wipe failed: %v", errWipe)
	}

	// Verify table gone
	err = s.DB().QueryRowContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_name = 'registry'").Scan(&tableName)
	if err == nil {
		t.Error("Table 'registry' should not exist after wipe, but query succeeded")
	}
	// Note: sql.ErrNoRows might handle this, or it returns empty result set depending on driver.
	// Actually queryRow returns error if no rows.
}
