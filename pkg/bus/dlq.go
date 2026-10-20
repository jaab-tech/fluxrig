// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Dead letter reasons. They name why a message was routed rather than
// processed, so an operator can tell a malformed payload from a handler that
// could never succeed.
const (
	DLQReasonDecode              = "decode"
	DLQReasonValidate            = "validate"
	DLQReasonRedeliveryExhausted = "redelivery-exhausted"
)

// Header keys on a dead letter. The failure context lives here so the payload
// stays byte-identical to what failed and can be replayed as-is.
const (
	dlqHeaderOriginSubject = "Flux-DLQ-Origin-Subject"
	dlqHeaderReason        = "Flux-DLQ-Reason"
	dlqHeaderDeliveries    = "Flux-DLQ-Deliveries"
	dlqHeaderAt            = "Flux-DLQ-At"
)

// dlqDefaultSubject is the prefix used when no DLQ subject is configured.
const dlqDefaultSubject = "flux.dlq"

// deadLetterSubject builds the subject a failed message is republished to.// The origin subject is appended so an operator can filter the DLQ by where
// the message was headed, and replaying a subject's failures is one read:
// flux.dlq.<origin>.
func deadLetterSubject(prefix, originSubject string) string {
	if prefix == "" {
		prefix = dlqDefaultSubject
	}
	return strings.TrimSuffix(prefix, ".") + "." + originSubject
}

// deliveryCount reads how many times the server has delivered this message.
// It returns 0 when the metadata is unavailable, which is not treated as
// exhaustion: a message with no count still gets its redeliveries.
func deliveryCount(msg jetstream.Msg) uint64 {
	meta, err := msg.Metadata()
	if err != nil {
		return 0
	}
	return meta.NumDelivered
}

// routeToDLQ republishes the original, unchanged bytes to the dead letter
// stream, with the failure context in headers. It returns the publish error,
// if any, so the caller can decide what to do with the original message: a
// message that cannot be decoded will not decode on redelivery either, so the
// caller settles the original regardless, but it must log a DLQ failure rather
// than let it pass silently.
func routeToDLQ(ctx context.Context, js jetstream.JetStream, prefix, originSubject string, data []byte, reason string, deliveries uint64) error {
	if js == nil {
		return fmt.Errorf("dead letter queue unavailable: bus not connected")
	}
	subject := deadLetterSubject(prefix, originSubject)

	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header: nats.Header{
			dlqHeaderOriginSubject: []string{originSubject},
			dlqHeaderReason:        []string{reason},
			dlqHeaderDeliveries:    []string{fmt.Sprintf("%d", deliveries)},
			dlqHeaderAt:            []string{time.Now().UTC().Format(time.RFC3339Nano)},
		},
	}

	_, err := js.PublishMsg(ctx, msg,
		jetstream.WithMsgID(fmt.Sprintf("dlq-%s-%d", originSubject, time.Now().UnixNano())),
	)
	if err != nil {
		return fmt.Errorf("failed to publish to dead letter subject %s: %w", subject, err)
	}

	slog.Warn("NATS Bus: message routed to dead letter queue",
		"origin_subject", originSubject,
		"dlq_subject", subject,
		"reason", reason,
		"deliveries", deliveries,
		"bytes", len(data),
	)
	return nil
}
