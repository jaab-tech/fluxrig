// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

// The catalog scenario fails for the wrong reason if the module does not pass
// the sandbox check an import runs.
func TestModulePassesTheSandbox(t *testing.T) {
	key, err := pki.GenerateClusterKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cm, err := catalog.NewCatalogManager(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "c"), filepath.Join(dir, "k"), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cm.Import(context.Background(), minimalModule(), true); err != nil {
		t.Fatalf("the module does not import: %v", err)
	}
}
