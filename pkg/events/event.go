// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// StreamName is the JetStream stream that holds durable events.
	StreamName = "flux-events"
	// SubjectRoot is the first part of every event subject. It is not under
	// flux.ctrl because two streams cannot overlap, and the message stream
	// captures flux.ctrl.>.
	SubjectRoot = "flux.evt"
	// DefaultStreamMaxAge is how long the stream keeps an event.
	DefaultStreamMaxAge = time.Hour
)

// The events of a connection.
const (
	// Opened reports a connection that can take a reply at once.
	Opened = "opened"
	// Closed reports a connection that left the gear. It comes once, with a reason.
	Closed = "closed"
)

// The reasons of a Closed event.
const (
	ReasonPeerClosed      = "peer_closed"
	ReasonIdleTimeout     = "idle_timeout"
	ReasonReadError       = "read_error"
	ReasonInvalidFrame    = "invalid_frame"
	ReasonClosedByCommand = "closed_by_command"
	ReasonStopped         = "stopped"
)

// ConnEvent is the body of a connection event, as JSON.
type ConnEvent struct {
	Event    string    `json:"event"`
	ConnID   string    `json:"conn_id"`
	Gear     string    `json:"gear"`
	Rack     string    `json:"rack"`
	PeerIP   string    `json:"peer_ip"`
	PeerPort string    `json:"peer_port"`
	TS       time.Time `json:"ts"`
	Reason   string    `json:"reason,omitempty"`
}

// id is stable for one event, so a retry of a publish does not store it twice.
func (e ConnEvent) id() uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.ConnID+"."+e.Event))
}

// ConnSubject returns the subject of the connection events of a gear on a Rack.
func ConnSubject(rack, gear string) string {
	return fmt.Sprintf("%s.conn.%s.%s", SubjectRoot, rack, gear)
}

var notDurableName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// DurableName returns the name of the durable consumer of a reader. Each reader
// on each Rack has its own, so readers never compete for an event. A consumer
// name has a restricted character set, so any other character becomes "_".
func DurableName(rack, reader, source string) string {
	return notDurableName.ReplaceAllString(strings.Join([]string{rack, reader, source}, "_"), "_")
}
