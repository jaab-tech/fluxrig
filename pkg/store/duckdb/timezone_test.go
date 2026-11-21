// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// A telemetry time reads back as the instant it was written, whatever the
// zone of the host: queries compare it with the clock in UTC. Run with a TZ
// other than UTC, it failed by the host's offset.
func TestTelemetryTimesAreUTC(t *testing.T) {
	t.Setenv("TZ", "America/Montevideo")
	store, err := NewStore(slog.Default(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = store.DB().Exec(`INSERT INTO telemetry_logs (timestamp, body) VALUES (to_timestamp(?/1000000.0), 'x')`, now.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err = store.DB().QueryRow(`SELECT timestamp FROM telemetry_logs`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if d := got.Sub(now); d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("wrote %s, read %s: off by %s", now, got.UTC(), d)
	}
}
