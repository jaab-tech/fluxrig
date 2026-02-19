// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
)

// NATSControlPlane implements ControlPlane using NATS.
type NATSControlPlane struct {
	nc *nats.Conn
}

// NewNATSControlPlane creates a new instance.
func NewNATSControlPlane(nc *nats.Conn) *NATSControlPlane {
	return &NATSControlPlane{nc: nc}
}

// Publish sends a command to "flux.ctrl.{targetGearID}".
func (cp *NATSControlPlane) Publish(targetGearID string, cmd Command) error {
	subject := fmt.Sprintf("flux.ctrl.%s", targetGearID)
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal control command: %w", err)
	}
	return cp.nc.Publish(subject, data)
}

// Subscribe listens on "flux.ctrl.{myGearID}" and returns a channel of Commands.
func (cp *NATSControlPlane) Subscribe(myGearID string) (<-chan Command, error) {
	subject := fmt.Sprintf("flux.ctrl.%s", myGearID)
	ch := make(chan Command, 100) // Buffered channel

	_, err := cp.nc.Subscribe(subject, func(msg *nats.Msg) {
		var cmd Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			log.Printf("ERROR: Failed to unmarshal control command on %s: %v", subject, err)
			return
		}
		ch <- cmd
	})

	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to control plane %s: %w", subject, err)
	}

	return ch, nil
}
