// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Store manages the Mixer's local DuckDB: telemetry, and the topology rows
// derived from the active scenario. The registry of Racks, Mixers and Snakes
// lives in the JetStream key-value registry (package kvstore).
type Store struct {
	db        *sql.DB
	dataDir   string       // Directory containing the DB file and telemetry
	isMemory  bool         // true for an ephemeral :memory: store (nothing to lock across processes)
	log       *slog.Logger // Logger for store operations
	migrateMu sync.Mutex   // guards Migrate's version-check-then-apply sequence
}

// NewStore opens a DuckDB database file.
func NewStore(log *slog.Logger, path string) (*Store, error) {
	dsn := path
	if dsn == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping duckdb: %w", err)
	}
	dataDir := "data"
	if path != "" && path != ":memory:" {
		dataDir = filepath.Dir(path)
	}
	return &Store{db: db, dataDir: dataDir, isMemory: dsn == ":memory:", log: log.With("component", "STORE")}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

// mapError translates database errors to registry package errors.
func (s *Store) mapError(err error) error {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return registry.ErrNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "Constraint Error") || strings.Contains(msg, "unique constraint") {
		return registry.ErrNameConflict
	}
	return err
}

// Telemetry Queries (registry.Registry)

func (s *Store) QueryLogs(ctx context.Context, q registry.LogQuery) ([]registry.LogEntry, error) {
	internalLogs, err := s.QueryLogsFiltered(ctx, LogQuery{
		Limit:    q.Limit,
		Entity:   q.EntityName,
		MinLevel: q.MinLevel,
		Since:    q.Since,
		Until:    q.Until,
	})
	if err != nil {
		return nil, err
	}
	entries := make([]registry.LogEntry, len(internalLogs))
	for i, l := range internalLogs {
		entries[i] = registry.LogEntry{
			Timestamp:  l.Timestamp,
			EntityName: l.EntityName,
			Severity:   l.Severity,
			Body:       l.Body,
			Attributes: l.Attributes,
		}
	}
	return entries, nil
}

func (s *Store) QueryMetrics(ctx context.Context, q registry.MetricQuery) ([]registry.MetricEntry, error) {
	internalMetrics, err := s.QueryMetricsFiltered(ctx, MetricQuery{
		Limit:  q.Limit,
		Entity: q.EntityName,
		Name:   q.Name,
		Since:  q.Since,
		Until:  q.Until,
	})
	if err != nil {
		return nil, err
	}
	entries := make([]registry.MetricEntry, len(internalMetrics))
	for i, m := range internalMetrics {
		entries[i] = registry.MetricEntry{
			Timestamp:  m.Timestamp,
			EntityName: m.EntityName,
			Name:       m.Name,
			Type:       m.Type,
			Value:      m.Value,
			Attributes: m.Attributes,
		}
	}
	return entries, nil
}

// Mixed Registry Operations (Legacy/Internal)

func (s *Store) GetEntityIDByName(ctx context.Context, name string) (uuid.UUID, error) {
	var eid uuid.UUID
	err := s.db.QueryRowContext(ctx, "SELECT entity_id FROM registry WHERE name = ?", name).Scan(&eid)
	return eid, s.mapError(err)
}

func (s *Store) RegisterScenario(ctx context.Context, eid uuid.UUID, name, version string, gearCount, wireCount int, mixerID uuid.UUID) error {
	s.log.Info("Registering Scenario", "eid", eid, "name", name, "gears", gearCount, "wires", wireCount, "mixer_id", mixerID)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 10", name); errDel != nil {
		return errDel
	}
	attrs, _ := json.Marshal(map[string]any{"gear_count": gearCount, "wire_count": wireCount})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, mixer_id, name, status, version, first_seen, last_seen, stats, attributes)
		VALUES (?, 10, ?, ?, 'active', ?, ?, ?, ?, ?)
	`, eid, mixerID, name, version, now, now, "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) RegisterGear(ctx context.Context, eid uuid.UUID, name, gearType, mode, bind, connect string, scenarioID uuid.UUID, machineID uuid.UUID, ports map[string]uuid.UUID, mixerID uuid.UUID) error {
	s.log.Info("Registering Gear", "eid", eid, "name", name, "type", gearType, "scenario_id", scenarioID, "machine_id", machineID)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 5", name); errDel != nil {
		return errDel
	}
	pMap := make(map[string]string)
	for k, v := range ports {
		pMap[k] = v.String()
	}
	attrs, _ := json.Marshal(map[string]any{
		"gear_type": gearType, "mode": mode, "bind": bind, "connect": connect, "scenario_id": scenarioID.String(), "ports": pMap,
	})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, first_seen, last_seen, stats, attributes)
		VALUES (?, 5, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, machineID, mixerID, name, now, now, "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) RegisterWire(ctx context.Context, eid uuid.UUID, fromName, toName string, fromID, toID uuid.UUID, scenarioID uuid.UUID, machineID uuid.UUID, mixerID uuid.UUID) error {
	s.log.Info("Registering Wire", "eid", eid, "from", fromName, "to", toName, "scenario_id", scenarioID, "machine_id", machineID)
	now := time.Now()
	name := fmt.Sprintf("%s->%s", fromName, toName)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 8", name); errDel != nil {
		return errDel
	}
	attrs, _ := json.Marshal(map[string]any{"from": fromID.String(), "to": toID.String(), "scenario_id": scenarioID.String()})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, first_seen, last_seen, stats, attributes)
		VALUES (?, 8, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, machineID, mixerID, name, now, now, "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) RegisterPort(ctx context.Context, eid uuid.UUID, name string, portType uint16, gearID uuid.UUID, scenarioID uuid.UUID, machineID uuid.UUID, mixerID uuid.UUID) error {
	s.log.Info("Registering Port", "eid", eid, "name", name, "type", portType, "gear_id", gearID, "scenario_id", scenarioID)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = ?", name, portType)
	attrs, _ := json.Marshal(map[string]any{"gear": gearID.String()})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, first_seen, last_seen, stats, attributes)
		VALUES (?, ?, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, portType, machineID, mixerID, name, now, now, "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) ClearScenarioEntities(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE type_id IN (5, 6, 7, 8, 10)")
	return s.mapError(err)
}

// Telemetry Logic (Internal)

func (s *Store) FlushTelemetry(ctx context.Context, dataDir string) error {
	tables := map[string]string{"telemetry_logs": "logs", "telemetry_spans": "spans", "telemetry_metrics": "metrics"}
	ts := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for table, dirName := range tables {
		dir := filepath.Join(dataDir, "telemetry", dirName, ts.Format("2006/01/02/15"))
		_ = os.MkdirAll(dir, 0750)
		path := filepath.Join(dir, fmt.Sprintf("%s_%d.parquet", dirName, ts.UnixNano()))
		var count int
		// table is a key of the tables map above, never external input: an
		// identifier position, which a placeholder cannot fill in DuckDB or
		// any SQL dialect. The file path is a value, and is bound as one.
		_ = tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count)
		if count == 0 {
			continue
		}
		if _, errExport := tx.ExecContext(ctx, fmt.Sprintf("COPY (SELECT * FROM %s) TO ? (FORMAT 'parquet', COMPRESSION 'zstd')", table), path); errExport != nil {
			return errExport
		}
		if _, errTrunc := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", table)); errTrunc != nil {
			return errTrunc
		}
	}
	return tx.Commit()
}

func (s *Store) FlushArchiverBuffer(ctx context.Context, dataDir string) error {
	ts := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT wire_id FROM archiver_buffer")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	// Scanned as uuid.UUID, not string: this driver returns a UUID column's
	// raw 16 bytes for a *string destination, not its canonical hyphenated
	// form. Formatted straight into the old query text, those raw bytes
	// produced a WHERE clause that could never match the row it came from,
	// so a wire's archived messages were never actually being flushed by id.
	var wireIDs []uuid.UUID
	for rows.Next() {
		var wid uuid.UUID
		if errScan := rows.Scan(&wid); errScan == nil {
			wireIDs = append(wireIDs, wid)
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return errRows
	}
	for _, wid := range wireIDs {
		dir := filepath.Join(dataDir, "messages", wid.String(), ts.Format("2006/01/02/15"))
		_ = os.MkdirAll(dir, 0750)
		path := filepath.Join(dir, fmt.Sprintf("messages_%s_%d.parquet", wid, ts.UnixNano()))
		// wire_id and the file path are both values, bound as parameters
		// rather than formatted into the query text. Named, not positional:
		// a COPY with two "?" placeholders spanning the subquery and the TO
		// clause binds them out of their textual order in this driver;
		// $name avoids that ambiguity entirely.
		if _, errExport := tx.ExecContext(ctx,
			"COPY (SELECT ts, flux_id, trace_id, subject, payload, meta FROM archiver_buffer WHERE wire_id = $wire_id) TO $path (FORMAT 'parquet', COMPRESSION 'zstd')",
			sql.Named("wire_id", wid), sql.Named("path", path)); errExport != nil {
			return errExport
		}
	}
	_, errTrunc := tx.ExecContext(ctx, "DELETE FROM archiver_buffer")
	if errTrunc != nil {
		return errTrunc
	}
	return tx.Commit()
}

func (s *Store) QueryLogsFiltered(ctx context.Context, q LogQuery) ([]TelemetryLog, error) {
	if q.Since.IsZero() {
		q.Since = time.Now().Add(-24 * time.Hour)
	}

	// 1. Determine if we have Parquet files to include
	logsDir := filepath.Join(s.dataDir, "telemetry", "logs")
	parquetGlob := filepath.Join(logsDir, "**", "*.parquet")
	hasParquet := false
	_ = filepath.Walk(logsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(path) == ".parquet" {
			hasParquet = true
			return filepath.SkipDir
		}
		return nil
	})

	// 2. Build Query
	var query string
	if hasParquet {
		query = `
			SELECT timestamp, entity_name, severity, body, attributes FROM (
				SELECT timestamp, entity_name, severity, body, attributes FROM telemetry_logs
				UNION ALL
				SELECT timestamp, entity_name, severity, body, attributes FROM read_parquet('` + parquetGlob + `')
			) WHERE timestamp >= ?`
	} else {
		query = "SELECT timestamp, entity_name, severity, body, attributes FROM telemetry_logs WHERE timestamp >= ? "
	}

	args := []any{q.Since}
	if !q.Until.IsZero() {
		query += " AND timestamp <= ? "
		args = append(args, q.Until)
	}
	if q.Entity != "" {
		query += " AND entity_name = ?"
		args = append(args, q.Entity)
	}
	if q.MinLevel != "" {
		query += " AND severity = ?"
		args = append(args, strings.ToUpper(q.MinLevel))
	}

	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, s.mapError(err)
	}
	defer func() { _ = rows.Close() }()

	logs := []TelemetryLog{} // Non-nil empty slice
	for rows.Next() {
		var l TelemetryLog
		var attrStr string
		if errScan := rows.Scan(&l.Timestamp, &l.EntityName, &l.Severity, &l.Body, &attrStr); errScan != nil {
			return nil, errScan
		}
		_ = json.Unmarshal([]byte(attrStr), &l.Attributes)
		logs = append(logs, l)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, errRows
	}
	return logs, nil
}

func (s *Store) QueryMetricsFiltered(ctx context.Context, q MetricQuery) ([]TelemetryMetric, error) {
	if q.Since.IsZero() {
		q.Since = time.Now().Add(-24 * time.Hour)
	}

	// 1. Determine if we have Parquet files to include
	metricsDir := filepath.Join(s.dataDir, "telemetry", "metrics")
	parquetGlob := filepath.Join(metricsDir, "**", "*.parquet")
	hasParquet := false
	_ = filepath.Walk(metricsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(path) == ".parquet" {
			hasParquet = true
			return filepath.SkipDir
		}
		return nil
	})

	// 2. Build Query
	var query string
	if hasParquet {
		query = `
			SELECT timestamp, entity_name, name, type, value, attributes FROM (
				SELECT timestamp, entity_name, name, type, value, attributes FROM telemetry_metrics
				UNION ALL
				SELECT timestamp, entity_name, name, type, value, attributes FROM read_parquet('` + parquetGlob + `')
			) WHERE timestamp >= ?`
	} else {
		query = "SELECT timestamp, entity_name, name, type, value, attributes FROM telemetry_metrics WHERE timestamp >= ? "
	}

	args := []any{q.Since}
	if q.Entity != "" {
		query += " AND entity_name = ?"
		args = append(args, q.Entity)
	}
	if q.Name != "" {
		query += " AND name = ?"
		args = append(args, q.Name)
	}

	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, s.mapError(err)
	}
	defer func() { _ = rows.Close() }()

	metrics := []TelemetryMetric{} // Non-nil empty slice
	for rows.Next() {
		var m TelemetryMetric
		var attrStr string
		if errScan := rows.Scan(&m.Timestamp, &m.EntityName, &m.Name, &m.Type, &m.Value, &attrStr); errScan != nil {
			return nil, errScan
		}
		_ = json.Unmarshal([]byte(attrStr), &m.Attributes)
		metrics = append(metrics, m)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, errRows
	}
	return metrics, nil
}

type TelemetryMetric struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Value      float64        `json:"value"`
	Attributes map[string]any `json:"attributes"`
}

type TelemetryLog struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Severity   string         `json:"severity"`
	Body       string         `json:"body"`
	Attributes map[string]any `json:"attributes"`
}

type LogQuery struct {
	Limit    int
	Entity   string
	MinLevel string
	Since    time.Time
	Until    time.Time
}

type MetricQuery struct {
	Limit  int
	Entity string
	Name   string
	Since  time.Time
	Until  time.Time
}
