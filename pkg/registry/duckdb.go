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

package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/store/duckdb"
)

type DuckDBRegistry struct {
	store *duckdb.Store
}

func NewDuckDBRegistry(s *duckdb.Store) *DuckDBRegistry {
	return &DuckDBRegistry{store: s}
}

// Register handles Rack registration and re-registration.
// Modified to accept version.
// Register handles Rack registration and re-registration.
// Modified to accept version.
func (r *DuckDBRegistry) Register(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*Rack, error) {
	// ... (omitting comments for brevity in replacement chunk matching if needed, but keeping logic)
	status := "active"
	prefix := "node-"

	if name == "" {
		name = fmt.Sprintf("%spending-%d", prefix, time.Now().UnixNano())
		status = "pending"
	} else if strings.Contains(name, "pending-") || strings.Contains(name, "node-") || isZeroConfigPrefix(name) {
		status = "pending"
	}

	existing, err := r.getByName(ctx, name)
	if err == nil {
		if existing.Secret != "" && secret != existing.Secret {
			return nil, ErrNameConflict
		}
		// Update IP, Port, LastSeen, and Config
		return r.updateSeen(ctx, existing.MachineID, ip, port, config)
	}

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
	statsJSON, _ := json.Marshal(stats)
	configJSON, _ := json.Marshal(config)

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
	statsJSON, _ := json.Marshal(rack.Stats)
	configJSON, _ := json.Marshal(rack.Config)
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
		entityID, machineID, mixerID, newName, "0.0.0", rack.FirstSeen, now, string(statsJSON), string(configJSON), string(attrsJSON),
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
	statsJSON, _ := json.Marshal(rack.Stats)
	configJSON, _ := json.Marshal(rack.Config)

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
	row := r.store.DB().QueryRowContext(ctx, "SELECT machine_id, name, status, version, started_at, last_seen, stats, config, attributes FROM registry WHERE name = ? AND type_id = 4", name)
	var i Rack
	var attributesAny any
	var statsAny any
	var configAny any
	var version sql.NullString
	if err := row.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &configAny, &attributesAny); err != nil {
		return nil, err
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

func (r *DuckDBRegistry) updateSeen(ctx context.Context, id uint16, ip string, port int, config map[string]any) (*Rack, error) {
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

	configJSON, _ := json.Marshal(config)

	now := time.Now()
	_, err = r.store.DB().ExecContext(ctx, "UPDATE registry SET last_seen = ?, config = ?, attributes = ? WHERE machine_id = ? AND type_id = 4", now, string(configJSON), string(attrsJSON), id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

func isZeroConfigPrefix(name string) bool {
	// Simple heuristic check if needed, but not used currently due to consolidation above
	return false
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
