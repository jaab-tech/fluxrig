// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"time"
)

// Rack represents a registered node in the cluster.
type Rack struct {
	MachineID uint16    `json:"machine_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // active, pending, offline
	IP        string    `json:"ip"`
	Port      int       `json:"port"` // Added: Support NAT/Localhost
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Statistics (Transient/Snapshot)
	Stats map[string]any `json:"stats"`

	// Configuration (Runtime)
	Config map[string]any `json:"config"`

	// Security
	Secret string `json:"-"` // Internal only, do not expose in API
}

// Registry defines the contract for managing Rack identities.
type Registry interface {
	// Register handles the initial connection of a Rack.
	// If name is empty or reserved prefix "node-", it is treated as ephemeral/pending.
	// Returns the assigned MachineID and Name.
	// Validates secret if provided. Generates new secret if new enrollment.
	Register(ctx context.Context, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uint64) (*Rack, error)

	// Approve adopts a pending Rack by assigning it a permanent name.
	Approve(ctx context.Context, machineID uint16, newName string) (*Rack, error)

	// List returns all Racks matching the status filter (empty = all).
	List(ctx context.Context, status string) ([]*Rack, error)

	// Get retrieves a single rack by ID.
	Get(ctx context.Context, machineID uint16) (*Rack, error)

	// Heartbeat updates the last_seen timestamp and transient stats.
	Heartbeat(ctx context.Context, machineID uint16, stats map[string]any, config map[string]any) error

	// Remove deletes a rack from the registry.
	Remove(ctx context.Context, machineID uint16) error

	// UpdateStatus changes the status of a rack (e.g. suspend/activate).
	UpdateStatus(ctx context.Context, machineID uint16, status string) error

	// Telemetry Queries
	QueryLogs(ctx context.Context, query LogQuery) ([]LogEntry, error)
	QueryMetrics(ctx context.Context, query MetricQuery) ([]MetricEntry, error)
}

// LogQuery defines filters for querying logs.
type LogQuery struct {
	Limit      int       `json:"limit"`
	MinLevel   string    `json:"min_level"`   // DEBUG, INFO, WARN, ERROR
	EntityName string    `json:"entity_name"` // Exact match or prefix? Store implements glob/like.
	Since      time.Time `json:"since"`       // Start time
	Until      time.Time `json:"until"`       // End time
}

// MetricQuery defines filters for querying metrics.
type MetricQuery struct {
	Limit      int       `json:"limit"`
	Name       string    `json:"name"`        // Metric name
	EntityName string    `json:"entity_name"` // Entity name
	Since      time.Time `json:"since"`       // Start time
	Until      time.Time `json:"until"`       // End time
}

// LogEntry represents a log record from telemetry.
type LogEntry struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Severity   string         `json:"severity"`
	Body       string         `json:"body"`
	Attributes map[string]any `json:"attributes"`
}

// MetricEntry represents a metric point.
type MetricEntry struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Value      float64        `json:"value"`
	Attributes map[string]any `json:"attributes"`
}

var (
	ErrNameConflict = errors.New("rack name already taken")
	ErrNotFound     = errors.New("rack not found")
)
