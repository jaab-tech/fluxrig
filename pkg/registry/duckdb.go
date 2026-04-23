// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

type DuckDBRegistry struct {
	store     *duckdb.Store
	autoAdopt bool
}

func NewDuckDBRegistry(store *duckdb.Store) *DuckDBRegistry {
	return &DuckDBRegistry{
		store:     store,
		autoAdopt: false,
	}
}

func (r *DuckDBRegistry) SetAutoAdopt(enabled bool) {
	r.autoAdopt = enabled
}

// Register handles Rack registration and re-registration.
// Modified to accept version.
// Register handles Rack registration and re-registration.
// Modified to accept version.
func (r *DuckDBRegistry) Register(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*Rack, error) {
	// ... (omitting comments for brevity in replacement chunk matching if needed, but keeping logic)
	status := "active"
	if !r.autoAdopt {
		status = "pending"
	}

	if name == "" {
		name = fmt.Sprintf("node-pending-%d", time.Now().UnixNano())
		status = "pending"
	}

	// Check name conflict (across all types)
	existing, err := r.getByName(ctx, name)
	if err == nil {
		// Strict Security: If the name exists, you MUST provide the correct secret to claim/re-enroll it.
		// This applies to both 'active' and 'pending' states to prevent hijacking.
		if secret != "" && secret == existing.Secret {
			// Update IP, Port, LastSeen, Version, and Config
			return r.updateSeen(ctx, existing.MachineID, ip, port, version, config)
		}
		return nil, ErrNameConflict
	}
	// Note: sql.ErrNoRows is expected here for new enrollments

	var id uint16
	row := r.store.DB().QueryRowContext(ctx, "SELECT nextval('seq_machine_id_server')")
	if errScan := row.Scan(&id); errScan != nil {
		return nil, fmt.Errorf("failed to generate id: %w", errScan)
	}

	secretBytes := make([]byte, 32)
	if _, errRand := rand.Read(secretBytes); errRand != nil {
		return nil, fmt.Errorf("failed to generate secret: %w", errRand)
	}
	newSecret := hex.EncodeToString(secretBytes)

	gen, _ := idgen.New(id)
	entityID := gen.NewEntityID(idgen.EntityRack, 0)
	now := time.Now()
	stats := map[string]any{}

	attrs := map[string]any{"secret": newSecret}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(stats)); errJSON == nil && string(b) != "null" {
		statsJSON = b
	}
	configJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(config)); errJSON == nil && string(b) != "null" {
		configJSON = b
	}

	_, err = r.store.DB().ExecContext(ctx,
		"INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, config, attributes) VALUES (?, 4, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		entityID, id, mixerID, name, status, version, now, now, string(statsJSON), string(configJSON), string(attrsJSON),
	)
	if err != nil {
		return nil, err
	}

	return &Rack{
		MachineID: id,
		Name:      name,
		Status:    status,
		IP:        ip,
		Port:      port,
		FirstSeen: now,
		LastSeen:  now,
		Stats:     stats,
		Config:    config,
		Secret:    newSecret,
	}, nil
}

func (r *DuckDBRegistry) Approve(ctx context.Context, machineID uint16, newName string) (*Rack, error) {
	// Check name conflict
	if existing, err := r.getByName(ctx, newName); err == nil {
		// If existing rack found, check if it's the same rack (allow self-rename)
		if existing.MachineID != machineID {
			return nil, ErrNameConflict
		}
	}

	rack, err := r.Get(ctx, machineID)
	if err != nil {
		return nil, err
	}

	// Carry over existing Stats/Config or let Get handle it.
	// Synchronize configuration using the provided rack object.
	attrs := map[string]any{
		"secret": rack.Secret,
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(rack.Stats)); errJSON == nil && string(b) != "null" {
		statsJSON = b
	}
	configJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(rack.Config)); errJSON == nil && string(b) != "null" {
		configJSON = b
	}
	now := time.Now()

	// 1. Get EntityID and MixerID
	var entityID uint64
	var mixerID uint64
	err = r.store.DB().QueryRowContext(ctx, "SELECT entity_id, mixer_id FROM registry WHERE machine_id = ? AND type_id = 4", machineID).Scan(&entityID, &mixerID)
	if err != nil {
		return nil, err
	}

	// 2. Delete (Auto-Commit)
	_, err = r.store.DB().ExecContext(ctx, "DELETE FROM registry WHERE entity_id = ?", entityID)
	if err != nil {
		return nil, err
	}

	// 3. Insert (Auto-Commit)
	_, err = r.store.DB().ExecContext(ctx,
		"INSERT INTO registry (entity_id, type_id, machine_id, mixer_id, name, status, version, started_at, last_seen, stats, config, attributes) VALUES (?, 4, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)",
		entityID, machineID, mixerID, newName, rack.Version, rack.FirstSeen, now, string(statsJSON), string(configJSON), string(attrsJSON),
	)
	if err != nil {
		return nil, err
	}

	return r.Get(ctx, machineID)
}

func (r *DuckDBRegistry) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any, config map[string]any) error {

	// Heartbeat updates stats column directly.
	// We fetch current stats first to merge? Or just patch?
	// To perform a merge, we need the old stats.
	rack, err := r.Get(ctx, machineID)
	if err != nil {
		return err
	}

	// Merge new stats into existing stats
	if rack.Stats == nil {
		rack.Stats = make(map[string]any)
	}
	for k, v := range stats {
		rack.Stats[k] = v
	}

	// Update config if provided
	if config != nil {
		rack.Config = config
	}

	// Re-pack
	statsJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(rack.Stats)); errJSON == nil && string(b) != "null" {
		statsJSON = b
	}
	configJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(rack.Config)); errJSON == nil && string(b) != "null" {
		configJSON = b
	}

	res, err := r.store.DB().ExecContext(ctx,
		"UPDATE registry SET last_seen = ?, stats = ?, config = ? WHERE machine_id = ? AND type_id = 4",
		time.Now(), string(statsJSON), string(configJSON), machineID,
	)
	if err != nil {
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *DuckDBRegistry) Remove(ctx context.Context, machineID uint16) error {
	res, err := r.store.DB().ExecContext(ctx, "DELETE FROM registry WHERE machine_id = ? AND type_id = 4", machineID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *DuckDBRegistry) UpdateStatus(ctx context.Context, machineID uint16, status string) error {
	res, err := r.store.DB().ExecContext(ctx, "UPDATE registry SET status = ?, last_seen = ? WHERE machine_id = ? AND type_id = 4", status, time.Now(), machineID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *DuckDBRegistry) List(ctx context.Context, statusFilter string) ([]*Rack, error) {
	racks := []*Rack{} // Initialize to empty slice to ensure JSON [] instead of null
	query := "SELECT machine_id, name, status, version, started_at, last_seen, stats, config, attributes FROM registry WHERE type_id = 4"
	args := []any{}

	if statusFilter != "" {
		query += " AND status = ?"
		args = append(args, statusFilter)
	}

	query += " ORDER BY machine_id ASC"

	rows, err := r.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var i Rack
		var attributesAny any
		var statsAny any
		var configAny any
		var version sql.NullString
		// DuckDB's JSON type may scan as string or []byte depending on the driver version.
		// We scan into interface{} and handle type assertion below.

		if err := rows.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &configAny, &attributesAny); err != nil {
			return nil, err
		}
		if version.Valid {
			i.Version = version.String
		}

		// Handle Attributes unmarshaling
		var attrs map[string]any
		if m, ok := attributesAny.(map[string]any); ok {
			attrs = m
		} else if b, ok := attributesAny.([]byte); ok {
			_ = json.Unmarshal(b, &attrs)
		} else if s, ok := attributesAny.(string); ok {
			_ = json.Unmarshal([]byte(s), &attrs)
		}

		// Unpack Attributes to Rack fields for compatibility
		if val, ok := attrs["ip"].(string); ok {
			i.IP = val
		}
		if val, ok := attrs["port"].(float64); ok {
			i.Port = int(val)
		} // JSON numbers are float64
		if val, ok := attrs["secret"].(string); ok {
			i.Secret = val
		}

		// Handle Stats unmarshaling
		var stats map[string]any
		if m, ok := statsAny.(map[string]any); ok {
			stats = m
		} else if b, ok := statsAny.([]byte); ok {
			_ = json.Unmarshal(b, &stats)
		} else if s, ok := statsAny.(string); ok {
			_ = json.Unmarshal([]byte(s), &stats)
		}
		i.Stats = stats

		// Handle Config unmarshaling
		var regConfig map[string]any
		if m, ok := configAny.(map[string]any); ok {
			regConfig = m
		} else if b, ok := configAny.([]byte); ok {
			_ = json.Unmarshal(b, &regConfig)
		} else if s, ok := configAny.(string); ok {
			_ = json.Unmarshal([]byte(s), &regConfig)
		}
		i.Config = regConfig

		racks = append(racks, &i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return racks, nil
}

func (r *DuckDBRegistry) Get(ctx context.Context, id uint16) (*Rack, error) {
	row := r.store.DB().QueryRowContext(ctx, "SELECT machine_id, name, status, version, started_at, last_seen, stats, config, attributes FROM registry WHERE machine_id = ? AND type_id = 4", id)
	var i Rack
	var attributesAny any
	var statsAny any
	var configAny any
	var version sql.NullString
	if err := row.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &configAny, &attributesAny); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if version.Valid {
		i.Version = version.String
	}

	// Handle Attributes unmarshaling
	var attrs map[string]any
	if m, ok := attributesAny.(map[string]any); ok {
		attrs = m
	} else if b, ok := attributesAny.([]byte); ok {
		_ = json.Unmarshal(b, &attrs)
	} else if s, ok := attributesAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &attrs)
	}

	// Unpack Attributes
	if val, ok := attrs["ip"].(string); ok {
		i.IP = val
	}
	if val, ok := attrs["port"].(float64); ok {
		i.Port = int(val)
	}
	if val, ok := attrs["secret"].(string); ok {
		i.Secret = val
	}

	// Handle Stats unmarshaling
	var stats map[string]any
	if m, ok := statsAny.(map[string]any); ok {
		stats = m
	} else if b, ok := statsAny.([]byte); ok {
		_ = json.Unmarshal(b, &stats)
	} else if s, ok := statsAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &stats)
	}
	i.Stats = stats

	// Handle Config unmarshaling
	var regConfig map[string]any
	if m, ok := configAny.(map[string]any); ok {
		regConfig = m
	} else if b, ok := configAny.([]byte); ok {
		_ = json.Unmarshal(b, &regConfig)
	} else if s, ok := configAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &regConfig)
	}
	i.Config = regConfig

	return &i, nil
}

// Helpers

func (r *DuckDBRegistry) getByName(ctx context.Context, name string) (*Rack, error) {
	// Query WITHOUT type_id filter to catch global name conflicts
	row := r.store.DB().QueryRowContext(ctx, "SELECT type_id, machine_id, name, status, version, started_at, last_seen, stats, config, attributes FROM registry WHERE name = ?", name)
	var typeID int
	var i Rack
	var attributesAny any
	var statsAny any
	var configAny any
	var version sql.NullString
	if err := row.Scan(&typeID, &i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &configAny, &attributesAny); err != nil {
		return nil, err
	}
	if version.Valid {
		i.Version = version.String
	}

	// If it exists but it's not a Rack, it's always a conflict
	if typeID != 4 {
		return nil, ErrNameConflict
	}
	// Handle Attributes unmarshaling
	var attrs map[string]any
	if m, ok := attributesAny.(map[string]any); ok {
		attrs = m
	} else if b, ok := attributesAny.([]byte); ok {
		_ = json.Unmarshal(b, &attrs)
	} else if s, ok := attributesAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &attrs)
	}

	// Unpack Attributes
	if val, ok := attrs["ip"].(string); ok {
		i.IP = val
	}
	if val, ok := attrs["port"].(float64); ok {
		i.Port = int(val)
	}
	if val, ok := attrs["secret"].(string); ok {
		i.Secret = val
	}

	// Handle Stats unmarshaling
	var stats map[string]any
	if m, ok := statsAny.(map[string]any); ok {
		stats = m
	} else if b, ok := statsAny.([]byte); ok {
		_ = json.Unmarshal(b, &stats)
	} else if s, ok := statsAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &stats)
	}
	i.Stats = stats

	// Handle Config unmarshaling
	var regConfig map[string]any
	if m, ok := configAny.(map[string]any); ok {
		regConfig = m
	} else if b, ok := configAny.([]byte); ok {
		_ = json.Unmarshal(b, &regConfig)
	} else if s, ok := configAny.(string); ok {
		_ = json.Unmarshal([]byte(s), &regConfig)
	}
	i.Config = regConfig

	return &i, nil
}

func (r *DuckDBRegistry) updateSeen(ctx context.Context, id uint16, ip string, port int, version string, config map[string]any) (*Rack, error) {
	// Need to fetch, update attr, save.
	rack, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// We do NOT persist IP/Port here as they belong to the Snake (transport layer).
	// We only persist the secret and update the timestamp.
	attrs := map[string]any{
		"secret": rack.Secret,
	}
	attrsJSON, _ := json.Marshal(attrs)

	configJSON := []byte("{}")
	if b, errJSON := json.Marshal(cleanMap(config)); errJSON == nil && string(b) != "null" {
		configJSON = b
	}

	now := time.Now()
	_, err = r.store.DB().ExecContext(ctx, "UPDATE registry SET last_seen = ?, version = ?, config = ?, attributes = ? WHERE machine_id = ? AND type_id = 4", now, version, string(configJSON), string(attrsJSON), id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

func (r *DuckDBRegistry) QueryLogs(ctx context.Context, query LogQuery) ([]LogEntry, error) {
	storeQuery := duckdb.LogQuery{
		Limit:    query.Limit,
		MinLevel: query.MinLevel,
		Entity:   query.EntityName,
		Since:    query.Since,
		Until:    query.Until,
	}

	rows, err := r.store.QueryLogsFiltered(ctx, storeQuery)
	if err != nil {
		return nil, err
	}
	res := make([]LogEntry, len(rows))
	for i, row := range rows {
		res[i] = LogEntry{
			Timestamp:  row.Timestamp,
			EntityName: row.EntityName,
			Severity:   row.Severity,
			Body:       row.Body,
			Attributes: row.Attributes,
		}
	}
	return res, nil
}

func (r *DuckDBRegistry) QueryMetrics(ctx context.Context, query MetricQuery) ([]MetricEntry, error) {
	storeQuery := duckdb.MetricQuery{
		Limit:  query.Limit,
		Name:   query.Name,
		Entity: query.EntityName,
		Since:  query.Since,
		Until:  query.Until,
	}

	rows, err := r.store.QueryMetricsFiltered(ctx, storeQuery)
	if err != nil {
		return nil, err
	}
	res := make([]MetricEntry, len(rows))
	for i, row := range rows {
		res[i] = MetricEntry{
			Timestamp:  row.Timestamp,
			EntityName: row.EntityName,
			Name:       row.Name,
			Type:       row.Type,
			Value:      row.Value,
			Attributes: row.Attributes,
		}
	}
	return res, nil
}

func cleanMap(m interface{}) interface{} {
	switch v := m.(type) {
	case map[interface{}]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[fmt.Sprint(k)] = cleanMap(val)
		}
		return res
	case map[string]interface{}:
		res := make(map[string]interface{})
		for k, val := range v {
			res[k] = cleanMap(val)
		}
		return res
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, val := range v {
			res[i] = cleanMap(val)
		}
		return res
	default:
		return v
	}
}
