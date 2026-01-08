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
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/marcboeker/go-duckdb"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Store manages the DuckDB connection.
type Store struct {
	db      *sql.DB
	dataDir string       // Directory containing the DB file and telemetry
	log     *slog.Logger // Logger for store operations
}

// NewStore opens a DuckDB database file.
func NewStore(log *slog.Logger, path string) (*Store, error) {
	// If path is empty, use in-memory
	dsn := path
	if dsn == "" {
		dsn = ":memory:" // Standard in-memory
	}

	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping duckdb: %w", err)
	}

	// Derive dataDir from path
	dataDir := "data"
	if path != "" && path != ":memory:" {
		dataDir = filepath.Dir(path)
	}

	return &Store{db: db, dataDir: dataDir, log: log.With("component", "STORE")}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying sql.DB instance.
func (s *Store) DB() *sql.DB {
	return s.db
}

// InitializeSchema removed (Use Migrate)
// InitializeTelemetrySchema removed (Use Migrate)

// Wipe drops all tables. internal use for testing.
func (s *Store) Wipe(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS registry; DROP SEQUENCE IF EXISTS seq_machine_id_server;")
	return err
}

// FlushTelemetry exports the current buffer tables to Parquet files and clears them.
func (s *Store) FlushTelemetry(ctx context.Context, dataDir string) error {
	tables := map[string]string{
		"telemetry_logs":    "logs",
		"telemetry_spans":   "spans",
		"telemetry_metrics": "metrics",
	}
	ts := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for table, dirName := range tables {
		dir := filepath.Join(dataDir, dirName, ts.Format("2006/01/02/15"))
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", dir, err)
		}

		filename := fmt.Sprintf("%s_%d.parquet", dirName, ts.UnixNano())
		path := filepath.Join(dir, filename)

		var count int
		row := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table))
		if err := row.Scan(&count); err != nil {
			return err
		}

		if count == 0 {
			continue
		}

		//nolint:gosec
		query := fmt.Sprintf("COPY (SELECT * FROM %s) TO '%s' (FORMAT 'parquet', COMPRESSION 'zstd')", table, path)
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("failed to export %s: %w", table, err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			return fmt.Errorf("failed to truncate %s: %w", table, err)
		}
	}

	return tx.Commit()
}

// InitializeArchiverSchema removed (Use Migrate)

// FlushArchiverBuffer exports buffered messages to Parquet, partitioned by WireID.
func (s *Store) FlushArchiverBuffer(ctx context.Context, dataDir string) error {
	ts := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Get Distinct WireIDs with data
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT wire_id FROM archiver_buffer")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var wireIDs []uint64
	for rows.Next() {
		var wid uint64
		if err := rows.Scan(&wid); err == nil {
			wireIDs = append(wireIDs, wid)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	if len(wireIDs) == 0 {
		return nil // Nothing to flush
	}

	// 2. Export each WireID to its own folder structure
	for _, wid := range wireIDs {
		// Path: data/messages/<wire_id>/YYYY/MM/DD/HH
		dir := filepath.Join(dataDir, "messages", fmt.Sprintf("%d", wid), ts.Format("2006/01/02/15"))
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", dir, err)
		}

		filename := fmt.Sprintf("messages_%d_%d.parquet", wid, ts.UnixNano())
		path := filepath.Join(dir, filename)

		// SQL: COPY (SELECT ...) TO 'path'
		query := fmt.Sprintf( //nolint:gosec
			"COPY (SELECT ts, flux_id, trace_id, subject, payload, meta FROM archiver_buffer WHERE wire_id = %d) TO '%s' (FORMAT 'parquet', COMPRESSION 'zstd')",
			wid, path)

		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("failed to export wire %d: %w", wid, err)
		}
	}

	// 3. Truncate Buffer
	if _, err := tx.ExecContext(ctx, "DELETE FROM archiver_buffer"); err != nil {
		return fmt.Errorf("failed to truncate archiver_buffer: %w", err)
	}

	return tx.Commit()
}

// RegisterMixer upserts the Mixer's status in the registry.
func (s *Store) RegisterMixer(ctx context.Context, mid uint16, name string, eid uint64, addr, version string) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 2", name); errDel != nil {
		return errDel
	}

	attrs := map[string]any{
		"api_address": addr,
	}
	attrsJSON, _ := json.Marshal(attrs)

	stats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"cpu_cores":  runtime.NumCPU(),
	}
	statsJSON, _ := json.Marshal(stats)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, config, attributes)
		VALUES (?, 2, ?, NULL, ?, 'active', ?, ?, ?, ?, ?, ?)
	`, eid, mid, name, version, now, now, string(statsJSON), "{}", string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// RegisterSnake upserts a Snake (Link) into the registry.
func (s *Store) RegisterSnake(ctx context.Context, name string, eid uint64, version string, fromRack uint64, toMixer uint64, rackIP string, rackPort int, mixerIP string, mixerPort int, machineID uint16) error {
	now := time.Now()
	// No Tx needed? Use same DELETE+INSERT logic as Approve for consistency?
	// But Snake registration is high frequency. It uses Transactions currently.
	// Let's stick to Tx for now, assuming no PK collision on unrelated entities.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ? AND type_id = 9", eid); errDel != nil {
		return errDel
	}

	attrs := map[string]any{
		"rack":       fmt.Sprintf("%d", fromRack),
		"mixer":      fmt.Sprintf("%d", toMixer),
		"rack_ip":    rackIP,
		"rack_port":  rackPort,
		"mixer_ip":   mixerIP,
		"mixer_port": mixerPort,
	}
	attrsJSON, _ := json.Marshal(attrs)
	stats := map[string]any{}
	statsJSON, _ := json.Marshal(stats)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, attributes)
		VALUES (?, 9, ?, ?, ?, 'active', ?, ?, ?, ?, ?)
	`, eid, machineID, toMixer, name, version, now, now, string(statsJSON), string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// UpdateSnakeStats updates the stats column for a given Snake.
func (s *Store) UpdateSnakeStats(ctx context.Context, eid uint64, stats map[string]any) error {
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	now := time.Now()

	res, err := s.db.ExecContext(ctx, `
		UPDATE registry 
		SET stats = ?, last_seen = ? 
		WHERE entity_id = ? AND type_id = 9
	`, string(statsJSON), now, eid)

	if err != nil {
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("snake not found: %d", eid)
	}
	return nil
}

// ClearSnakes removes all snake entities (used on startup).
func (s *Store) ClearSnakes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE type_id = 9")
	return err
}

// RemoveSnake deletes a Snake from the registry.
func (s *Store) RemoveSnake(ctx context.Context, eid uint64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ? AND type_id = 9", eid)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("snake not found: %d", eid)
	}
	return nil
}

// GetEntityIDByName resolves an entity name to its ID.
func (s *Store) GetEntityIDByName(ctx context.Context, name string) (uint64, error) {
	var eid uint64
	row := s.db.QueryRowContext(ctx, "SELECT entity_id FROM registry WHERE name = ?", name)
	if err := row.Scan(&eid); err != nil {
		return 0, err
	}
	return eid, nil
}

// RegisterScenario upserts a Scenario into the registry.
func (s *Store) RegisterScenario(ctx context.Context, eid uint64, name, version string, gearCount, wireCount int, mixerID uint64) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 10", name); errDel != nil {
		return errDel
	}

	attrs := map[string]any{
		"gear_count": gearCount,
		"wire_count": wireCount,
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(map[string]any{})

	// Extract MachineID from EntityID (Type 9 [8] + MachineID [16] + Sequence [40])
	// But currently we don't have utility here.
	mid := uint16((eid >> 40) & 0xFFFF) //nolint:gosec

	// Wait, if eid was generated with seq 0?, mid is 0?
	// The idgen uses:
	// id := (uint64(idType) << 56) | (uint64(machineID) << 40) | (seq & 0xFFFFFFFFFF)
	// So (eid >> 40) & 0xFFFF extracts machineID correctly.

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, attributes)
		VALUES (?, 10, ?, ?, ?, 'active', ?, ?, ?, ?, ?)
	`, eid, mid, mixerID, name, version, now, now, string(statsJSON), string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// RegisterGear upserts a Gear into the registry.
func (s *Store) RegisterGear(ctx context.Context, eid uint64, name, gearType, mode, bind, connect string, scenarioID uint64, machineID uint16, ports map[string]uint64, mixerID uint64) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 5", name); errDel != nil {
		return errDel
	}

	attrs := map[string]any{
		"gear_type":   gearType,
		"mode":        mode,
		"bind":        bind,
		"connect":     connect,
		"scenario_id": fmt.Sprintf("%d", scenarioID),
		"ports":       ports,
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(map[string]any{})

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, attributes)
		VALUES (?, 5, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, machineID, mixerID, name, now, now, string(statsJSON), string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// RegisterWire upserts a Wire into the registry.
func (s *Store) RegisterWire(ctx context.Context, eid uint64, fromName, toName string, fromID, toID uint64, scenarioID uint64, machineID uint16, mixerID uint64) error {
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

	attrs := map[string]any{
		"from":        fmt.Sprintf("%d", fromID),
		"to":          fmt.Sprintf("%d", toID),
		"scenario_id": fmt.Sprintf("%d", scenarioID),
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(map[string]any{})

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, attributes)
		VALUES (?, 8, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, machineID, mixerID, name, now, now, string(statsJSON), string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// RegisterPort upserts a Port into the registry.
func (s *Store) RegisterPort(ctx context.Context, eid uint64, name string, portType uint16, gearID uint64, scenarioID uint64, machineID uint16, mixerID uint64) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Delete Existing
	_, err = tx.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = ?", name, portType)
	if err != nil {
		return err
	}

	attrs := map[string]any{
		"gear": fmt.Sprintf("%d", gearID),
		// scenario_id removed from schema
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(map[string]any{})

	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, attributes)
		VALUES (?, ?, ?, ?, ?, 'active', '', ?, ?, ?, ?)
	`, eid, portType, machineID, mixerID, name, now, now, string(statsJSON), string(attrsJSON))
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ActivateRack promotes a rack from 'pending' to 'active' status.
func (s *Store) ActivateRack(ctx context.Context, rackName string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE registry 
		SET status = 'active', last_seen = ? 
		WHERE name = ? AND type_id = 4 AND status = 'pending'
	`, time.Now(), rackName)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// Already active or not found - not an error
		return nil
	}
	return nil
}

// GetRackByName retrieves a rack's machine_id by name.
func (s *Store) GetRackByName(ctx context.Context, rackName string) (machineID uint16, err error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT machine_id FROM registry 
		WHERE name = ? AND type_id = 4
	`, rackName)
	if err := row.Scan(&machineID); err != nil {
		return 0, err
	}
	return machineID, nil
}

// ClearScenarioEntities removes all Gear, Wire, and Scenario entities.
func (s *Store) ClearScenarioEntities(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE type_id IN (5, 6, 7, 8, 10)")
	return err
}

// TelemetryLog represents a log entry.
type TelemetryLog struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Severity   string         `json:"severity"`
	Body       string         `json:"body"`
	Attributes map[string]any `json:"attributes"`
}

// LogQuery defines filters for querying logs.
type LogQuery struct {
	Limit    int
	Entity   string    // Optional: filter by entity name/pattern
	MinLevel string    // Optional: minimum severity (ERROR, WARN, INFO, DEBUG)
	Since    time.Time // Time filter (if zero, defaults to 24h ago)
	Until    time.Time // Optional: end time (defaults to now)
}

// QueryLogsFiltered retrieves logs with comprehensive filtering.
// Defaults to last 24 hours if no time filter specified.
func (s *Store) QueryLogsFiltered(ctx context.Context, q LogQuery) ([]TelemetryLog, error) {
	// Apply 24-hour default if no time filter
	if q.Since.IsZero() {
		q.Since = time.Now().Add(-24 * time.Hour)
		slog.Info("Query logs defaulting to last 24 hours", "since", q.Since)
	}

	// Build WHERE conditions
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "timestamp >= ?")
	args = append(args, q.Since)

	if !q.Until.IsZero() {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, q.Until)
	}
	if q.Entity != "" {
		conditions = append(conditions, "entity_name LIKE ?")
		args = append(args, q.Entity)
	}
	if q.MinLevel != "" {
		// Map severity to numeric for comparison
		severityOrder := map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}
		if minOrder, ok := severityOrder[q.MinLevel]; ok {
			var levels []string
			for level, order := range severityOrder {
				if order >= minOrder {
					levels = append(levels, "'"+level+"'")
				}
			}
			conditions = append(conditions, "severity IN ("+strings.Join(levels, ",")+")")
		}
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	query := ""
	logsDir := filepath.Join(s.dataDir, "telemetry", "logs")
	if hasParquetFiles(logsDir) {
		// Query both active table AND parquet files
		parquetPath := filepath.Join(logsDir, "**", "*.parquet")
		//nolint:gosec // whereClause built from validated params
		query = fmt.Sprintf(`
			SELECT timestamp, entity_name, severity, body, attributes FROM (
				SELECT timestamp, entity_name, severity, body, attributes FROM telemetry_logs
				UNION ALL
				SELECT timestamp, entity_name, severity, body, attributes FROM read_parquet('%s')
			) %s ORDER BY timestamp DESC LIMIT ?`,
			parquetPath, whereClause)
	} else {
		// Query only active table
		//nolint:gosec // whereClause built from validated params
		query = fmt.Sprintf(
			"SELECT timestamp, entity_name, severity, body, attributes FROM telemetry_logs %s ORDER BY timestamp DESC LIMIT ?",
			whereClause)
	}

	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var logs []TelemetryLog
	for rows.Next() {
		var l TelemetryLog
		var attrAny any
		if err := rows.Scan(&l.Timestamp, &l.EntityName, &l.Severity, &l.Body, &attrAny); err != nil {
			return nil, err
		}

		// Handle Attributes unmarshaling (DuckDB can return JSON as map or []byte)
		if m, ok := attrAny.(map[string]any); ok {
			l.Attributes = m
		} else if b, ok := attrAny.([]byte); ok && len(b) > 0 {
			_ = json.Unmarshal(b, &l.Attributes)
		} else if str, ok := attrAny.(string); ok && len(str) > 0 {
			_ = json.Unmarshal([]byte(str), &l.Attributes)
		}

		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}

// QueryLogs retrieves recent logs (backwards compatibility).
func (s *Store) QueryLogs(ctx context.Context, limit int) ([]TelemetryLog, error) {
	return s.QueryLogsFiltered(ctx, LogQuery{Limit: limit})
}

// TelemetryMetric represents a metric point.
type TelemetryMetric struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Value      float64        `json:"value"`
	Attributes map[string]any `json:"attributes"`
}

// MetricQuery defines filters for querying metrics.
type MetricQuery struct {
	Limit  int
	Entity string    // Optional: filter by entity name
	Name   string    // Optional: filter by metric name
	Since  time.Time // Time filter (if zero, defaults to 24h ago)
	Until  time.Time // Optional: end time
}

// QueryMetricsFiltered retrieves metrics with comprehensive filtering.
// Defaults to last 24 hours if no time filter specified.
func (s *Store) QueryMetricsFiltered(ctx context.Context, q MetricQuery) ([]TelemetryMetric, error) {
	// Apply 24-hour default if no time filter
	if q.Since.IsZero() {
		q.Since = time.Now().Add(-24 * time.Hour)
		slog.Info("Query metrics defaulting to last 24 hours", "since", q.Since)
	}

	// Build WHERE conditions
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "timestamp >= ?")
	args = append(args, q.Since)

	if !q.Until.IsZero() {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, q.Until)
	}
	if q.Entity != "" {
		conditions = append(conditions, "entity_name LIKE ?")
		args = append(args, q.Entity)
	}
	if q.Name != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, q.Name)
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	query := ""
	metricsDir := filepath.Join(s.dataDir, "metrics")
	if hasParquetFiles(metricsDir) {
		// Query both active table AND parquet files
		parquetPath := filepath.Join(metricsDir, "**", "*.parquet")
		//nolint:gosec // whereClause built from validated params
		query = fmt.Sprintf(`
			SELECT timestamp, entity_name, name, type, value, attributes FROM (
				SELECT timestamp, entity_name, name, type, value, attributes FROM telemetry_metrics
				UNION ALL
				SELECT timestamp, entity_name, name, type, value, attributes FROM read_parquet('%s', hive_partitioning=true)
			) %s ORDER BY timestamp DESC LIMIT ?`,
			parquetPath, whereClause)
	} else {
		// Query only active table
		//nolint:gosec // whereClause built from validated params
		query = fmt.Sprintf(
			"SELECT timestamp, entity_name, name, type, value, attributes FROM telemetry_metrics %s ORDER BY timestamp DESC LIMIT ?",
			whereClause)
	}

	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var metrics []TelemetryMetric
	for rows.Next() {
		var m TelemetryMetric
		var attrAny any
		if err := rows.Scan(&m.Timestamp, &m.EntityName, &m.Name, &m.Type, &m.Value, &attrAny); err != nil {
			return nil, err
		}

		// Handle Attributes unmarshaling (DuckDB can return JSON as map or []byte)
		if attrs, ok := attrAny.(map[string]any); ok {
			m.Attributes = attrs
		} else if b, ok := attrAny.([]byte); ok && len(b) > 0 {
			_ = json.Unmarshal(b, &m.Attributes)
		} else if s, ok := attrAny.(string); ok && len(s) > 0 {
			_ = json.Unmarshal([]byte(s), &m.Attributes)
		}

		metrics = append(metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return metrics, nil
}

// QueryMetrics retrieves recent metrics (backwards compatibility).
func (s *Store) QueryMetrics(ctx context.Context, limit int) ([]TelemetryMetric, error) {
	return s.QueryMetricsFiltered(ctx, MetricQuery{Limit: limit})
}

// hasParquetFiles checks if there are any .parquet files in the given directory or subdirectories.
func hasParquetFiles(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // ignore errors
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".parquet") {
			found = true
			return fmt.Errorf("found") // stop walking
		}
		return nil
	})
	return found
}
