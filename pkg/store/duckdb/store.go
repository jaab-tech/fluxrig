// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Store manages the DuckDB connection and implements registry.Registry.
type Store struct {
	db        *sql.DB
	dataDir   string       // Directory containing the DB file and telemetry
	log       *slog.Logger // Logger for store operations
	mu        sync.RWMutex
	autoAdopt bool
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
	return &Store{db: db, dataDir: dataDir, log: log.With("component", "STORE")}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) SetAutoAdopt(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoAdopt = enabled
}

func (s *Store) Wipe(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS registry; DROP SEQUENCE IF EXISTS seq_machine_id_server;")
	return err
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

// Identity & Enrollment (registry.Registry)

func (s *Store) Register(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	return s.RegisterEntity(ctx, 4, machineID, name, secret, ip, port, version, config, nil, mixerID)
}

func (s *Store) RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, attrs map[string]any, mixerID uuid.UUID) (*registry.Rack, error) {
	configJSON, _ := json.Marshal(config)
	attrsJSON, _ := json.Marshal(attrs)
	now := time.Now().UTC()

	s.log.Info("Registering Entity", "type_id", typeID, "machine_id", machineID, "name", name, "ip", ip, "port", port, "version", version)

	status := "pending"
	s.mu.RLock()
	if s.autoAdopt {
		status = "active"
	}
	s.mu.RUnlock()

	// Identity Verification Logic (MachineID based)
	var existingSecret string
	var existingName string
	// Explicitly use string comparison for UUID to avoid driver mapping issues in some DuckDB versions
	err := s.db.QueryRowContext(ctx, "SELECT secret, name FROM registry WHERE machine_id = ? AND type_id = ?", machineID.String(), typeID).Scan(&existingSecret, &existingName)
	if err != nil && err != sql.ErrNoRows {
		return nil, s.mapError(err)
	}

	if err == nil {
		// Entity exists with THIS MachineID, verify secret
		if existingSecret != "" && existingSecret != secret {
			return nil, errors.New("identity mismatch: incorrect secret for existing MachineID")
		}
		// Update (Preserve approved name from DB, don't overwrite with handshake name)
		_, err = s.db.ExecContext(ctx, `
			UPDATE registry SET 
				ip = ?, 
				port = ?, 
				last_seen = ?, 
				version = ?,
				config = ?,
				attributes = ?,
				update_count = update_count + 1
			WHERE machine_id = ? AND type_id = ?
		`, ip, port, now, version, string(configJSON), string(attrsJSON), machineID, typeID)
	} else {
		// New MachineID - Check if name is already taken by ANY entity (Global UNIQUE constraint)
		if name != "" {
			var conflictSecret string
			var conflictMachineID uuid.UUID
			var conflictTypeID uint8
			errName := s.db.QueryRowContext(ctx, "SELECT secret, machine_id, type_id FROM registry WHERE name = ?", name).Scan(&conflictSecret, &conflictMachineID, &conflictTypeID)
			if errName == nil {
				// Name exists. Verify if it's the SAME type and secret matches for adoption.
				if conflictTypeID == typeID && conflictSecret != "" && conflictSecret == secret {
					s.log.Info("Adopting existing name via Secret match", "name", name, "type_id", typeID, "old_machine_id", conflictMachineID, "new_machine_id", machineID)
					// Update existing record with NEW machineID (Adoption)
					_, err = s.db.ExecContext(ctx, `
						UPDATE registry SET 
							machine_id = ?,
							ip = ?, 
							port = ?, 
							last_seen = ?, 
							version = ?,
							config = ?,
							attributes = ?,
							update_count = update_count + 1
						WHERE name = ? AND type_id = ?
					`, machineID, ip, port, now, version, string(configJSON), string(attrsJSON), name, typeID)
					if err != nil {
						return nil, s.mapError(err)
					}
					return s.GetEntity(ctx, typeID, machineID)
				}
				// No match or no secret, or different type (Global Name Conflict)
				s.log.Warn("Registration Denied: Global name conflict", "name", name, "requested_type", typeID, "existing_type", conflictTypeID, "existing_machine_id", conflictMachineID)
				return nil, registry.ErrNameConflict
			} else if errName != sql.ErrNoRows {
				return nil, s.mapError(errName)
			}
		}

		// Fresh Registration
		if machineID == uuid.Nil {
			machineID, _ = uuid.NewV7()
			s.log.Info("Self-provisioning new MachineID", "name", name, "id", machineID)
		}

		// Generate v0.4.6 style EntityID (UUID v7 with metadata)
		eid, _ := uuid.NewV7()
		copy(eid[9:13], machineID[12:16]) // MachineID hint
		eid[13] = typeID                  // EntityType

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO registry (entity_id, type_id, machine_id, name, status, version, ip, port, secret, first_seen, last_seen, config, attributes, mixer_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, eid, typeID, machineID, name, status, version, ip, port, secret, now, now, string(configJSON), string(attrsJSON), mixerID)
	}

	if err != nil {
		return nil, s.mapError(err)
	}
	return s.GetEntity(ctx, typeID, machineID)
}

func (s *Store) Approve(ctx context.Context, id uuid.UUID, newName string) (*registry.Rack, error) {
	// Check if the entity exists
	var currentName string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM registry WHERE machine_id = ? AND type_id = 4", id).Scan(&currentName)
	if err != nil {
		return nil, s.mapError(err)
	}

	// If a new name is provided, check for conflicts with OTHER entities
	if newName != "" && newName != currentName {
		var conflict bool
		_ = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM registry WHERE name = ? AND machine_id != ?)", newName, id).Scan(&conflict)
		if conflict {
			return nil, registry.ErrNameConflict
		}
	} else {
		newName = currentName // Keep current name if empty
	}

	res, err := s.db.ExecContext(ctx, `UPDATE registry SET status = 'active', name = ? WHERE machine_id = ? AND type_id = 4`, newName, id)
	if err != nil {
		return nil, s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, registry.ErrNotFound
	}
	return s.Get(ctx, id)
}

func (s *Store) List(ctx context.Context, status string) ([]*registry.Rack, error) {
	query := `
		SELECT 
			machine_id, 
			COALESCE(name, ''), 
			COALESCE(status, ''), 
			COALESCE(version, ''), 
			COALESCE(ip, ''), 
			COALESCE(port, 0), 
			COALESCE(secret, ''), 
			COALESCE(first_seen, '1970-01-01 00:00:00'), 
			COALESCE(last_seen, '1970-01-01 00:00:00'), 
			COALESCE(update_count, 0),
			COALESCE(config, '{}'),
			COALESCE(stats, '{}')
		FROM registry WHERE type_id = 4`
	var args []any
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, s.mapError(err)
	}
	defer func() { _ = rows.Close() }()
	var racks []*registry.Rack
	for rows.Next() {
		var r registry.Rack
		var configStr, statsStr string
		if errScan := rows.Scan(&r.MachineID, &r.Name, &r.Status, &r.Version, &r.IP, &r.Port, &r.Secret, &r.FirstSeen, &r.LastSeen, &r.UpdateCount, &configStr, &statsStr); errScan != nil {
			return nil, errScan
		}
		_ = json.Unmarshal([]byte(configStr), &r.Config)
		_ = json.Unmarshal([]byte(statsStr), &r.Stats)
		racks = append(racks, &r)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, errRows
	}
	return racks, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*registry.Rack, error) {
	return s.GetEntity(ctx, 4, id)
}

func (s *Store) GetEntity(ctx context.Context, typeID uint8, id uuid.UUID) (*registry.Rack, error) {
	var r registry.Rack
	var configStr, statsStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT 
			machine_id, 
			COALESCE(name, ''), 
			COALESCE(status, ''), 
			COALESCE(version, ''), 
			COALESCE(ip, ''), 
			COALESCE(port, 0), 
			COALESCE(secret, ''), 
			COALESCE(first_seen, '1970-01-01 00:00:00'), 
			COALESCE(last_seen, '1970-01-01 00:00:00'), 
			COALESCE(update_count, 0),
			COALESCE(config, '{}'),
			COALESCE(stats, '{}')
		FROM registry WHERE machine_id = ? AND type_id = ?
	`, id, typeID).Scan(&r.MachineID, &r.Name, &r.Status, &r.Version, &r.IP, &r.Port, &r.Secret, &r.FirstSeen, &r.LastSeen, &r.UpdateCount, &configStr, &statsStr)
	if err != nil {
		return nil, s.mapError(err)
	}
	_ = json.Unmarshal([]byte(configStr), &r.Config)
	_ = json.Unmarshal([]byte(statsStr), &r.Stats)
	return &r, nil
}

func (s *Store) Heartbeat(ctx context.Context, id uuid.UUID, stats map[string]any, config map[string]any) error {
	return s.HeartbeatEntity(ctx, 4, id, stats, config)
}

func (s *Store) HeartbeatEntity(ctx context.Context, typeID uint8, id uuid.UUID, stats map[string]any, config map[string]any) error {
	statsJSON, _ := json.Marshal(stats)
	configJSON, _ := json.Marshal(config)
	res, err := s.db.ExecContext(ctx, `UPDATE registry SET last_seen = ?, stats = ?, config = ? WHERE machine_id = ? AND type_id = ?`, time.Now().UTC(), string(statsJSON), string(configJSON), id, typeID)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func (s *Store) Remove(ctx context.Context, id uuid.UUID) error {
	return s.RemoveEntity(ctx, 4, id)
}

func (s *Store) RemoveByName(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE name = ? AND type_id = 4", name)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func (s *Store) RemoveEntity(ctx context.Context, typeID uint8, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE machine_id = ? AND type_id = ?", id, typeID)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return registry.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return s.UpdateStatusEntity(ctx, 4, id, status)
}

func (s *Store) UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE registry SET status = ? WHERE machine_id = ? AND type_id = ?", status, id, typeID)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return registry.ErrNotFound
	}
	return nil
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

func (s *Store) RegisterMixer(ctx context.Context, mid uuid.UUID, name string, eid uuid.UUID, addr, version string) error {
	s.log.Info("Registering Mixer Component", "mid", mid, "name", name, "eid", eid, "addr", addr, "version", version)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ? AND type_id = 2", eid); errDel != nil {
		return errDel
	}
	attrs, _ := json.Marshal(map[string]any{"api_address": addr})
	stats, _ := json.Marshal(map[string]any{"goroutines": runtime.NumGoroutine(), "cpu_cores": runtime.NumCPU()})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, name, status, version, first_seen, last_seen, stats, config, attributes)
		VALUES (?, 2, ?, ?, 'active', ?, ?, ?, ?, ?, ?)
	`, eid, mid, name, version, now, now, string(stats), "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) RegisterSnake(ctx context.Context, name string, eid uuid.UUID, version string, fromRack uuid.UUID, toMixer uuid.UUID, rackIP string, rackPort int, mixerIP string, mixerPort int, machineID uuid.UUID) error {
	s.log.Info("Registering Snake Transport", "name", name, "eid", eid, "from_rack", fromRack, "to_mixer", toMixer, "machine_id", machineID)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, errDel := tx.ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ? AND type_id = 9", eid); errDel != nil {
		return errDel
	}
	attrs, _ := json.Marshal(map[string]any{
		"rack": fromRack.String(), "mixer": toMixer.String(), "rack_ip": rackIP, "rack_port": rackPort, "mixer_ip": mixerIP, "mixer_port": mixerPort,
	})
	_, errReg := tx.ExecContext(ctx, `
		INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, first_seen, last_seen, stats, attributes)
		VALUES (?, 9, ?, ?, ?, 'active', ?, ?, ?, ?, ?)
	`, eid, machineID, toMixer, name, version, now, now, "{}", string(attrs))
	if errReg != nil {
		return s.mapError(errReg)
	}
	return tx.Commit()
}

func (s *Store) UpdateSnakeStats(ctx context.Context, eid uuid.UUID, stats map[string]any) error {
	statsJSON, _ := json.Marshal(stats)
	res, err := s.db.ExecContext(ctx, `UPDATE registry SET stats = ?, last_seen = ? WHERE entity_id = ? AND type_id = 9`, string(statsJSON), time.Now(), eid)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("snake not found: %s", eid)
	}
	return nil
}

func (s *Store) ClearSnakes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE type_id = 9")
	return err
}

func (s *Store) RemoveSnake(ctx context.Context, eid uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ? AND type_id = 9", eid)
	if err != nil {
		return s.mapError(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("snake not found: %s", eid)
	}
	return nil
}

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

func (s *Store) ActivateRack(ctx context.Context, rackName string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE registry SET status = 'active', last_seen = ? WHERE name = ? AND type_id = 4 AND status = 'pending'`, time.Now(), rackName)
	return s.mapError(err)
}

func (s *Store) GetRackByName(ctx context.Context, rackName string) (machineID uuid.UUID, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT machine_id FROM registry WHERE name = ? AND type_id = 4`, rackName).Scan(&machineID)
	return machineID, s.mapError(err)
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
		_ = tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count)
		if count == 0 {
			continue
		}
		if _, errExport := tx.ExecContext(ctx, fmt.Sprintf("COPY (SELECT * FROM %s) TO '%s' (FORMAT 'parquet', COMPRESSION 'zstd')", table, path)); errExport != nil {
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
	var wireIDs []string
	for rows.Next() {
		var wid string
		if errScan := rows.Scan(&wid); errScan == nil {
			wireIDs = append(wireIDs, wid)
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return errRows
	}
	for _, wid := range wireIDs {
		dir := filepath.Join(dataDir, "messages", wid, ts.Format("2006/01/02/15"))
		_ = os.MkdirAll(dir, 0750)
		path := filepath.Join(dir, fmt.Sprintf("messages_%s_%d.parquet", wid, ts.UnixNano()))
		if _, errExport := tx.ExecContext(ctx, fmt.Sprintf("COPY (SELECT ts, flux_id, trace_id, subject, payload, meta FROM archiver_buffer WHERE wire_id = '%s') TO '%s' (FORMAT 'parquet', COMPRESSION 'zstd')", wid, path)); errExport != nil {
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
