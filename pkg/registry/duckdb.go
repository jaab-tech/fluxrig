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
func (r *DuckDBRegistry) Register(ctx context.Context, name string, secret string, ip string, port int, version string) (*Rack, error) {
	// If name is empty, it's a new Zero Config node
	// If name contains "pending-", it's pending adoption
	status := "active"
	// isZeroConfig := false // DISABLED: No Auto-Rename

	// Default prefix if name is totally empty
	prefix := "node-"

	// Name Resolution Strategy:
	// ...

	if name == "" {
		name = fmt.Sprintf("%spending-%d", prefix, time.Now().UnixNano())
		status = "pending"
		// isZeroConfig = true
	} else if idx := strings.Index(name, "pending-"); idx >= 0 {
		// prefix = name[:idx]
		status = "pending"
		// isZeroConfig = true
	} else if idx := strings.Index(name, "node-"); idx >= 0 {
		status = "pending"
		// isZeroConfig = true
	} else if isZeroConfigPrefix(name) {
		// Heuristic: Prefix match indicates unverified identity.
		status = "pending"
	}

	// Strict Mode Policy:
	// All dynamic/unverified enrollments default to 'pending' status until explicitly approved.
	// This ensures security by preventing unauthorized Racks from becoming active automatically.

	// Upsert logic:
	// 1. Try to find by Name
	existing, err := r.getByName(ctx, name)
	if err == nil {
		// SESSION SECURITY (TOFU)
		// If the existing record has a secret, we MUST verify it.
		// Exception: If new connection passes NO secret (e.g. fresh install trying to hijack), we reject.
		if existing.Secret != "" {
			if secret != existing.Secret {
				return nil, ErrNameConflict // Auth Failed
			}
		}

		// Update IP, Port, and LastSeen
		return r.updateSeen(ctx, existing.MachineID, ip, port)
	}

	// 2. Insert New
	// Generate new MachineID from sequence
	var id uint16
	row := r.store.DB().QueryRowContext(ctx, "SELECT nextval('seq_machine_id_server')")
	if err := row.Scan(&id); err != nil {
		return nil, fmt.Errorf("failed to generate id: %w", err)
	}

	/*
		// If zero config, append ID to prefix to make it unique e.g. "node-42"
		if isZeroConfig {
			// We use the extracted or default prefix
			name = fmt.Sprintf("%s%d", prefix, id)
		}
	*/

	// Generate Secret (32 bytes hex)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secret: %w", err)
	}
	newSecret := hex.EncodeToString(secretBytes)

	// Calculate EntityID (Type 3 = Rack)
	// We need an idgen instance. Since sequence is 0 for the Rack itself (it's the container), we pass 0.
	// Wait, internal idgen needs a machineID to init.
	// But here we are *assigning* the MachineID.
	// The EntityID is constructed as: [Type][MachineID][Seq=0]
	// idgen package has NewEntityID(type, seq). We need to verify if we can construct it manually or use idgen.
	// idgen.New(mid) -> then NewEntityID.
	gen, _ := idgen.New(id)
	entityID := gen.NewEntityID(idgen.EntityRack, 0)

	now := time.Now()
	// Initialize empty stats map
	stats := map[string]any{}

	// Insert into registry (Type 3 = Rack)
	// Pack attributes (exclude stats, they are separate now)
	attrs := map[string]any{
		"secret": newSecret, // FIX: Use generated secret, not input arg
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(stats)

	_, err = r.store.DB().ExecContext(ctx,
		"INSERT INTO registry (entity_id, type_id, machine_id, name, status, version, started_at, last_seen, stats, attributes) VALUES (?, 3, ?, ?, ?, ?, ?, ?, ?, ?)",
		entityID, id, name, status, version, now, now, string(statsJSON), string(attrsJSON),
	)
	if err != nil {
		// Handle race condition on Name uniqueness
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

	attrs := map[string]any{
		"secret": rack.Secret,
	}
	attrsJSON, _ := json.Marshal(attrs)
	statsJSON, _ := json.Marshal(rack.Stats)
	now := time.Now()

	// 1. Get EntityID
	var entityID uint64
	err = r.store.DB().QueryRowContext(ctx, "SELECT entity_id FROM registry WHERE machine_id = ? AND type_id = 3", machineID).Scan(&entityID)
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
		"INSERT INTO registry (entity_id, type_id, machine_id, name, status, version, started_at, last_seen, stats, attributes) VALUES (?, 3, ?, ?, 'active', ?, ?, ?, ?, ?)",
		entityID, machineID, newName, "0.0.0", rack.FirstSeen, now, string(statsJSON), string(attrsJSON),
	)
	if err != nil {
		return nil, err
	}

	return r.Get(ctx, machineID)
}

func (r *DuckDBRegistry) Heartbeat(ctx context.Context, machineID uint16, stats map[string]any) error {

	// Heartbeat updates stats column directly.
	// We fetch current stats first to merge? Or just patch?
	// To perform a merge, we need the old stats.
	rack, err := r.Get(ctx, machineID)
	if err != nil {
		return err
	}

	// Merge new stats into existing stats
	// If rack.Stats is nil, init it.
	if rack.Stats == nil {
		rack.Stats = make(map[string]any)
	}
	for k, v := range stats {
		rack.Stats[k] = v
	}

	// Re-pack stats
	statsJSON, _ := json.Marshal(rack.Stats)

	res, err := r.store.DB().ExecContext(ctx,
		"UPDATE registry SET last_seen = ?, stats = ? WHERE machine_id = ? AND type_id = 3",
		time.Now(), string(statsJSON), machineID,
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
	res, err := r.store.DB().ExecContext(ctx, "DELETE FROM registry WHERE machine_id = ? AND type_id = 3", machineID)
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
	res, err := r.store.DB().ExecContext(ctx, "UPDATE registry SET status = ?, last_seen = ? WHERE machine_id = ? AND type_id = 3", status, time.Now(), machineID)
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
	query := "SELECT machine_id, name, status, version, started_at, last_seen, stats, attributes FROM registry WHERE type_id = 3"
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
	defer rows.Close()

	for rows.Next() {
		var i Rack
		var attributesAny any
		var statsAny any
		var version sql.NullString
		// DuckDB's JSON type may scan as string or []byte depending on the driver version.
		// We scan into interface{} and handle type assertion below.

		if err := rows.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &attributesAny); err != nil {
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

		racks = append(racks, &i)
	}
	return racks, nil
}

func (r *DuckDBRegistry) Get(ctx context.Context, id uint16) (*Rack, error) {
	row := r.store.DB().QueryRowContext(ctx, "SELECT machine_id, name, status, version, started_at, last_seen, stats, attributes FROM registry WHERE machine_id = ? AND type_id = 3", id)
	var i Rack
	var attributesAny any
	var statsAny any
	var version sql.NullString
	if err := row.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &attributesAny); err != nil {
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

	return &i, nil
}

// Helpers

func (r *DuckDBRegistry) getByName(ctx context.Context, name string) (*Rack, error) {
	row := r.store.DB().QueryRowContext(ctx, "SELECT machine_id, name, status, version, started_at, last_seen, stats, attributes FROM registry WHERE name = ? AND type_id = 3", name)
	var i Rack
	var attributesAny any
	var statsAny any
	var version sql.NullString
	if err := row.Scan(&i.MachineID, &i.Name, &i.Status, &version, &i.FirstSeen, &i.LastSeen, &statsAny, &attributesAny); err != nil {
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

	return &i, nil
}

func (r *DuckDBRegistry) updateSeen(ctx context.Context, id uint16, ip string, port int) (*Rack, error) {
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

	now := time.Now()
	_, err = r.store.DB().ExecContext(ctx, "UPDATE registry SET last_seen = ?, attributes = ? WHERE machine_id = ? AND type_id = 3", now, string(attrsJSON), id)
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
