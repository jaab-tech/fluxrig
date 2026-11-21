// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
)

// A 503 from the JetStream API (a metadata leader election) and a request
// that no stream answers pass once the cluster settles. Any other API error,
// or an error that is not from JetStream, does not.
func TestUnavailable(t *testing.T) {
	electing := &jetstream.APIError{Code: 503, ErrorCode: 10008, Description: "JetStream system temporarily unavailable"}
	assert.True(t, Unavailable(fmt.Errorf("failed to create ephemeral consumer for x: %w", electing)))
	assert.True(t, Unavailable(fmt.Errorf("wrapped: %w", nats.ErrNoResponders)))
	assert.True(t, Unavailable(jetstream.ErrNoStreamResponse))

	assert.False(t, Unavailable(&jetstream.APIError{Code: 400, ErrorCode: 10012, Description: "insufficient resources"}))
	assert.False(t, Unavailable(errors.New("nats bus not connected")))
	assert.False(t, Unavailable(nil))
}
