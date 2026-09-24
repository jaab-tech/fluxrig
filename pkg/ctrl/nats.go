// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package ctrl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// ErrNoAck is returned by ConfirmedPublish when nothing acknowledged the
// command within ctx: either no gear is subscribed on the target subject at
// all, or one is but its queue was full and the command was dropped (see
// subscribeInto). Either way, the caller learns the command did not reach a
// gear, instead of only that NATS accepted the publish.
var ErrNoAck = errors.New("control plane: no listener acknowledged the command")

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

// ConfirmedPublish sends a command to targetGearID and waits, bounded by ctx,
// for a listener to acknowledge receiving it: something subscribed on
// "flux.ctrl.{targetGearID}" placed the command in its queue (see
// subscribeInto's reply-on-receipt below). It does not confirm the gear
// finished acting on the command, only that one is there and took it,
// exactly the gap that let handleSimControl report success with nobody
// listening: a bare Publish only confirms NATS accepted the send.
func ConfirmedPublish(ctx context.Context, nc *nats.Conn, targetGearID string, cmd Command) error {
	subject := fmt.Sprintf("flux.ctrl.%s", targetGearID)
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal control command: %w", err)
	}
	if _, err := nc.RequestWithContext(ctx, subject, data); err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %q", ErrNoAck, targetGearID)
		}
		return err
	}
	return nil
}

// Subscribe listens on "flux.ctrl.{myGearID}" and returns a channel of Commands.
func (cp *NATSControlPlane) Subscribe(myGearID string) (<-chan Command, error) {
	ch := make(chan Command, 100) // Buffered channel
	if _, err := subscribeInto(cp.nc, myGearID, ch); err != nil {
		return nil, err
	}
	return ch, nil
}

// subscribeInto delivers the commands sent to gearID on nc into ch, and returns the
// subscription so that it can be released. A command that finds ch full is dropped and
// logged: blocking here would hold the delivery of the subscription, and a gear that has
// stopped never reads its channel again.
func subscribeInto(nc *nats.Conn, gearID string, ch chan<- Command) (*nats.Subscription, error) {
	subject := fmt.Sprintf("flux.ctrl.%s", gearID)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var cmd Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			slog.Error("control plane unmarshal failed",
				"flux.subject", subject,
				"error", err,
			)
			return
		}
		select {
		case ch <- cmd:
			// Acknowledge only once the command is actually queued for the
			// gear: ConfirmedPublish's caller must learn a full queue the
			// same way it would learn no listener at all, not read a reply
			// as "delivered" when it was really dropped just below.
			if msg.Reply != "" {
				_ = nc.Publish(msg.Reply, nil)
			}
		default:
			slog.Warn("control plane: a listener's queue is full, command dropped", "flux.subject", subject, "cmd", cmd.Cmd)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to control plane %s: %w", subject, err)
	}
	return sub, nil
}
