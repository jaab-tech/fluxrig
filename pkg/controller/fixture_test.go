// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry/kvstore"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

// fixtureStore is the registry the scenario tests run against: Racks,
// scenario files and the active pointer in a KV registry over in-memory
// buckets, topology rows in an in-memory DuckDB.
type fixtureStore struct {
	*kvstore.Store
	topo *duckdb.Store
}

func newFixtureStore(log *slog.Logger, dsn string) (*fixtureStore, error) {
	topo, err := duckdb.NewStore(log, dsn)
	if err != nil {
		return nil, err
	}
	kv := kvstore.New(log, kvstore.NewMemoryBucket(0), kvstore.NewMemoryBucket(90*time.Second), topo, uuid.New(),
		kvstore.Config{MaxValueSize: 1 << 20, WriteRetries: 3})
	return &fixtureStore{Store: kv, topo: topo}, nil
}

func (f *fixtureStore) Migrate(ctx context.Context) error { return f.topo.Migrate(ctx) }

func (f *fixtureStore) GetEntityIDByName(ctx context.Context, name string) (uuid.UUID, error) {
	return f.topo.GetEntityIDByName(ctx, name)
}
