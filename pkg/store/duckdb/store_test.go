package duckdb

import (
	"context"
	"testing"
)

func TestStore_Lifecycle(t *testing.T) {
	// 1. NewStore (In-Memory)
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	if s.DB() == nil {
		t.Error("DB() returned nil")
	}

	ctx := context.Background()

	// 2. Initialize Schema
	if err := s.InitializeSchema(ctx); err != nil {
		t.Fatalf("InitializeSchema failed: %v", err)
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
	if err := s.Wipe(ctx); err != nil {
		t.Errorf("Wipe failed: %v", err)
	}

	// Verify table gone
	err = s.DB().QueryRowContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_name = 'registry'").Scan(&tableName)
	if err == nil {
		t.Error("Table 'registry' should not exist after wipe, but query succeeded")
	}
	// Note: sql.ErrNoRows might handle this, or it returns empty result set depending on driver.
	// Actually queryRow returns error if no rows.
}
