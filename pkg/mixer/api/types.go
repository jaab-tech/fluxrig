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

package api

import (
	"time"

	"github.com/jaab-tech/fluxrig/pkg/config"
)

// HealthResponse represents the health check response.
type HealthResponse struct {
	// Status of the service (e.g. "ok").
	Status string `json:"status" example:"ok"`
	// Version of the service.
	Version string `json:"version" example:"0.1.0"`
}

// RackResponse represents a Rack in the registry.
type RackResponse struct {
	// ID is the entity ID of the rack.
	ID uint64 `json:"id" example:"45612378"`
	// Name of the rack.
	Name string `json:"name" example:"rack-01"`
	// Status of the rack (active, inactive, pending).
	Status string `json:"status" example:"active"`
	// IP address of the rack agent.
	IP string `json:"ip" example:"192.168.1.50"`
	// Port number of the rack agent.
	Port int `json:"port" example:"4222"`
	// LastSeen timestamp.
	LastSeen time.Time `json:"last_seen" example:"2023-10-27T10:00:00Z"`
	// FirstSeen timestamp.
	FirstSeen time.Time `json:"first_seen" example:"2023-10-26T10:00:00Z"`
	// MachineID is the physical machine ID.
	MachineID uint16 `json:"machine_id" example:"10"`

	// Stats (Transient/Snapshot).
	Stats RackStats `json:"stats"`
	// Config (Runtime).
	Config config.RackConfig `json:"config"`
}

// RackStats represents the transient statistics of a Rack.
type RackStats struct {
	// CPU usage percentage.
	CpuUsage float64 `json:"cpu_usage" example:"12.5"`
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
