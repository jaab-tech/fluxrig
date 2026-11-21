// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

// HealthResponse represents the health check response.
type HealthResponse struct {
	// Status of the service (e.g. "ok").
	Status string `json:"status" example:"ok"`
	// Version of the service.
	Version string `json:"version" example:"0.1.0"`
	// MachineID is the persistent machine ID.
	MachineID uuid.UUID `json:"machine_id" example:"019de49b-0000-7000-8000-000000000000"`
	// EntityID is the unique fluxEntityID of this Mixer.
	EntityID uuid.UUID `json:"entity_id" example:"019de49b-0000-7000-8000-000000000000"`
	// Lease reports the write lease of a Mixer cluster. Absent when the lease
	// is off (a single Mixer).
	Lease *LeaseStatus `json:"lease,omitempty"`
}

// LeaseStatus reports which Mixer holds the write lease, as this Mixer reads
// it from the cluster.
type LeaseStatus struct {
	// Holder is the machine_id of the Mixer that holds the lease. Absent when
	// no Mixer holds it: control writes answer 503 until one does.
	Holder *uuid.UUID `json:"holder,omitempty" example:"019de49b-0000-7000-8000-000000000000"`
	// HolderSince is when the holder took the lease.
	HolderSince *time.Time `json:"holder_since,omitempty"`
	// Renewed is the holder's last successful renew.
	Renewed *time.Time `json:"renewed,omitempty"`
	// Held is true when this Mixer holds the lease and may write.
	Held bool `json:"held" example:"false"`
	// Error is "unavailable" when the lease could not be read. The raw cause
	// is not returned because /health does not require authentication.
	Error string `json:"error,omitempty"`
}

// RackResponse represents a Rack in the registry.
type RackResponse struct {
	// ID is the entity ID of the rack.
	ID uuid.UUID `json:"id" example:"45612378"`
	// Name of the rack.
	Name string `json:"name" example:"rack-01"`
	// Status of the rack (active, inactive, pending).
	Status string `json:"status" example:"active"`
	// IP address of the Rack.
	IP string `json:"ip" example:"192.168.1.50"`
	// Port the Rack listens on.
	Port int `json:"port" example:"4222"`
	// LastSeen timestamp.
	LastSeen time.Time `json:"last_seen" example:"2023-10-27T10:00:00Z"`
	// FirstSeen timestamp.
	FirstSeen time.Time `json:"first_seen" example:"2023-10-26T10:00:00Z"`
	// MachineID is the physical machine ID.
	MachineID uuid.UUID `json:"machine_id" example:"019de49b-0000-7000-8000-000000000000"`

	// Stats (Transient/Snapshot).
	Stats RackStats `json:"stats"`
	// Config (Runtime).
	Config config.RackConfig `json:"config"`
}

// RackStats represents the transient statistics of a Rack.
type RackStats struct {
	// CPU usage percentage.
	CPUUsage float64 `json:"cpu_usage" example:"12.5"`
	// Memory usage percentage.
	MemUsage float64 `json:"mem_usage" example:"45.2"`
	// Uptime in seconds.
	Uptime int64 `json:"uptime" example:"3600"`
	// Load average (1m).
	LoadAvg float64 `json:"load_avg" example:"0.5"`
}

// TopologyStatusResponse represents the fleet synchronization status.
type TopologyStatusResponse struct {
	// SyncStatus indicates global sync state.
	SyncStatus string `json:"sync_status" example:"synchronized"`
	// ActiveScenario is the name of the currently active scenario.
	ActiveScenario string `json:"active_scenario" example:"Getting Started"`
	// ActiveVer is the currently active scenario version.
	ActiveVer string `json:"active_ver" example:"v1.2.3"`
	// RacksTotal is the total number of registered racks.
	RacksTotal int `json:"racks_total" example:"50"`
}

// TopologyListResponse represents the projected topology.
type TopologyListResponse struct {
	// Racks list.
	Racks []RackResponse `json:"racks"`
	// Gears list (names).
	Gears []string `json:"gears" example:"iso8583-in,http-out"`
}

// ApproveRequest is the body for the approve action.
type ApproveRequest struct {
	// Name to assign to the rack (optional, defaults to current).
	Name string `json:"name" example:"rack-prod-01"`
}

// ActionResponse represents the result of a rack action.
type ActionResponse struct {
	// Status of the action.
	Status string `json:"status" example:"activated"`
	// Name of the rack (for import/approve).
	Name string `json:"name,omitempty" example:"rack-01"`
}

// ScenarioImportResponse represents the result of a scenario import.
type ScenarioImportResponse struct {
	// Status of the import.
	Status string `json:"status" example:"imported_and_activated"`
	// Name of the imported scenario.
	Name string `json:"name" example:"payment-switch-v1"`
}
