// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("entity not found")
	ErrNameConflict = errors.New("entity name already exists")
)

// Rack represents a physical or virtual compute node.
type Rack struct {
	MachineID   uuid.UUID      `json:"machine_id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"` // pending, active, offline
	Version     string         `json:"version"`
	IP          string         `json:"ip"`
	Port        int            `json:"port"`
	Secret      string         `json:"secret"`
	FirstSeen   time.Time      `json:"first_seen"`
	LastSeen    time.Time      `json:"last_seen"`
	UpdateCount int            `json:"update_count"`
	Stats       map[string]any `json:"stats"`
	Config      map[string]any `json:"config"`
}

// LogEntry represents a single telemetry log.
type LogEntry struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Severity   string         `json:"severity"`
	Body       string         `json:"body"`
	Attributes map[string]any `json:"attributes"`
}

// MetricEntry represents a single telemetry metric.
type MetricEntry struct {
	Timestamp  time.Time      `json:"timestamp"`
	EntityName string         `json:"entity_name"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Value      float64        `json:"value"`
	Attributes map[string]any `json:"attributes"`
}

// LogQuery defines filters for retrieving logs.
type LogQuery struct {
	Limit      int
	EntityName string
	MinLevel   string
	Since      time.Time
	Until      time.Time
}

// MetricQuery defines filters for retrieving metrics.
type MetricQuery struct {
	Limit      int
	EntityName string
	Name       string
	Since      time.Time
	Until      time.Time
}

// RegistryScenario represents an orchestrated workflow in the registry.
type RegistryScenario struct {
	EntityID  uuid.UUID `json:"entity_id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	GearCount int       `json:"gear_count"`
	WireCount int       `json:"wire_count"`
	Active    bool      `json:"active"`
}

// Registry defines the contract for identity and telemetry persistence.
type Registry interface {
	// Identity & Enrollment
	Register(ctx context.Context, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, mixerID uuid.UUID) (*Rack, error)
	RegisterEntity(ctx context.Context, typeID uint8, machineID uuid.UUID, name string, secret string, ip string, port int, version string, config map[string]any, attrs map[string]any, mixerID uuid.UUID) (*Rack, error)
	Approve(ctx context.Context, id uuid.UUID, newName string) (*Rack, error)
	List(ctx context.Context, status string) ([]*Rack, error)
	Get(ctx context.Context, id uuid.UUID) (*Rack, error)
	Heartbeat(ctx context.Context, id uuid.UUID, stats map[string]any, config map[string]any) error
	HeartbeatEntity(ctx context.Context, typeID uint8, id uuid.UUID, stats map[string]any, config map[string]any) error
	Remove(ctx context.Context, id uuid.UUID) error
	RemoveByName(ctx context.Context, name string) error
	RemoveEntity(ctx context.Context, typeID uint8, id uuid.UUID) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status string) error
	UpdateStatusEntity(ctx context.Context, typeID uint8, id uuid.UUID, status string) error
	SetAutoAdopt(enabled bool)

	// Telemetry Queries
	QueryLogs(ctx context.Context, q LogQuery) ([]LogEntry, error)
	QueryMetrics(ctx context.Context, q MetricQuery) ([]MetricEntry, error)

	// Maintenance
	ClearScenarioEntities(ctx context.Context) error
}
