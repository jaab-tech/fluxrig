// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

// Command matches the JSON schema defined in the control plane specification.
type Command struct {
	Cmd  string            `json:"cmd"`  // e.g., "conn.close"
	Args map[string]string `json:"args"` // e.g., {"conn_id": "123", "reason": "timeout"}
	Src  string            `json:"src"`  // e.g., "codec_iso8583"
}

// ControlPlane defines the interface for signaling.
type ControlPlane interface {
	// Publish sends a command to a specific target gear.
	Publish(targetGearID string, cmd Command) error

	// Subscribe listens for commands targeting this gear.
	Subscribe(myGearID string) (<-chan Command, error)
}

// Simulator control commands.
const (
	CmdSimStart = "sim.start"
	CmdSimStop  = "sim.stop"
	CmdSimRate  = "sim.rate"
	CmdSimReset = "sim.reset"
)

// SimStartArgs are arguments for sim.start.
type SimStartArgs struct {
	GearName string `json:"gear"`
	Seed     int64  `json:"seed,omitempty"`
}

// SimStopArgs are arguments for sim.stop.
type SimStopArgs struct {
	GearName string `json:"gear"`
}

// SimRateArgs are arguments for sim.rate.
type SimRateArgs struct {
	GearName string  `json:"gear"`
	TPS      float64 `json:"tps"`
	Shape    string  `json:"shape,omitempty"`
	From     float64 `json:"from,omitempty"`
	To       float64 `json:"to,omitempty"`
	Over     string  `json:"over,omitempty"`
}

// SimResetArgs are arguments for sim.reset.
type SimResetArgs struct {
	GearName string `json:"gear"`
	Seed     int64  `json:"seed,omitempty"`
}
