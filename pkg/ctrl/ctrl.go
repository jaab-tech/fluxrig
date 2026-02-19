// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

// Command matches the JSON schema defined in ADR 0020.
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
