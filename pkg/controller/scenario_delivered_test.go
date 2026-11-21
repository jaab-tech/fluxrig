// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/idgen"
)

func newDeliveryController(t *testing.T) *ScenarioController {
	t.Helper()
	gen, err := idgen.New(uuid.New())
	require.NoError(t, err)
	return NewScenarioController(slog.Default(), nil, nil, nil, gen, uuid.New(), time.Second)
}

// The delivery record is bounded: past the cap the oldest entries go, so a
// renamed or removed Rack leaves nothing behind forever.
func TestDelivered_OverTheCapTheOldestGo(t *testing.T) {
	c := newDeliveryController(t)
	c.deliveryMu.Lock()
	c.delivered = make(map[string]delivery)
	now := time.Now()
	for i := 0; i < maxDeliveredEntries+10; i++ {
		c.delivered[fmt.Sprintf("rack-%04d", i)] = delivery{activation: 1, at: now.Add(-time.Duration(i) * time.Minute)}
	}
	c.deliveryMu.Unlock()

	c.recordDelivery("rack-new")

	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	assert.LessOrEqual(t, len(c.delivered), maxDeliveredEntries)
	assert.Contains(t, c.delivered, "rack-new")
	// The freshest pre-existing entry survives; the oldest are gone.
	assert.Contains(t, c.delivered, "rack-0000")
	assert.NotContains(t, c.delivered, fmt.Sprintf("rack-%04d", maxDeliveredEntries+9))
}

// Entries older than the TTL are the first to go: the enrollment push only
// consults records newer than the enrollment, so they are never read again.
func TestDelivered_StaleEntriesAreEvictedFirst(t *testing.T) {
	c := newDeliveryController(t)
	c.deliveryMu.Lock()
	c.delivered = make(map[string]delivery)
	now := time.Now()
	for i := 0; i < maxDeliveredEntries; i++ {
		at := now
		if i%2 == 0 {
			at = now.Add(-deliveredTTL - time.Hour)
		}
		c.delivered[fmt.Sprintf("rack-%04d", i)] = delivery{activation: 1, at: at}
	}
	c.deliveryMu.Unlock()

	c.recordDelivery("rack-new")

	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	assert.LessOrEqual(t, len(c.delivered), maxDeliveredEntries)
	assert.Contains(t, c.delivered, "rack-new")
	for i := 0; i < maxDeliveredEntries; i += 2 {
		assert.NotContains(t, c.delivered, fmt.Sprintf("rack-%04d", i), "stale entries must go first")
	}
}
