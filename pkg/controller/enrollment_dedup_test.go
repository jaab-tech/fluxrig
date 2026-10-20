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
)

func dedupSize(c *EnrollmentController) int {
	n := 0
	c.processedHellos.Range(func(_, _ any) bool { n++; return true })
	return n
}

// A repeat within the window is throttled; after the window it is not.
func TestHelloThrottle_RepeatWithinWindow(t *testing.T) {
	clock := time.Unix(1000, 0)
	c := NewEnrollmentController(slog.Default(), nil, nil, nil, uuid.New(), time.Second)
	c.now = func() time.Time { return clock }

	assert.False(t, c.throttled("rack-a|id"))
	clock = clock.Add(helloThrottle / 2)
	assert.True(t, c.throttled("rack-a|id"), "a repeat inside the window is ignored")
	clock = clock.Add(helloThrottle)
	assert.False(t, c.throttled("rack-a|id"), "a repeat after the window is processed")
}

// Distinct keys do not accumulate: expired entries are removed. Without the
// sweep the map holds every key ever seen.
func TestHelloThrottle_ExpiredEntriesAreRemoved(t *testing.T) {
	clock := time.Unix(1000, 0)
	c := NewEnrollmentController(slog.Default(), nil, nil, nil, uuid.New(), time.Second)
	c.now = func() time.Time { return clock }

	for i := 0; i < 500; i++ {
		c.throttled(fmt.Sprintf("flood-%d|%s", i, uuid.Nil))
	}
	assert.Equal(t, 500, dedupSize(c))

	clock = clock.Add(2 * helloThrottle)
	c.throttled("late|id")
	assert.Equal(t, 1, dedupSize(c), "only the entry inside the window remains")
}
