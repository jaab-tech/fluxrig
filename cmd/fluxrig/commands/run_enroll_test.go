// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// A Rack whose Hello burst no Mixer answered keeps enrolling when it has no
// identity: nothing can adopt it, and its heartbeats would be rejected. A Rack
// with a saved identity resumes from its Passport instead.
func TestWaitsForPassport(t *testing.T) {
	assert.True(t, waitsForPassport(uuid.Nil), "no identity: keep sending Hello")
	assert.False(t, waitsForPassport(uuid.New()), "a saved identity: resume")
}
