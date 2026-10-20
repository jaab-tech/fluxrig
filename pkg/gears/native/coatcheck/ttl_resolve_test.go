// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// resolveTTL is the one place the store (KV entry TTL) and the daemon (timer)
// agree on an entry's expiry. It must return the message override when set,
// the default when not, and never more than max.
func TestResolveTTL(t *testing.T) {
	g := &CoatCheckGear{ctx: &mockGearContext{config: map[string]any{}}}
	def := 1 * time.Minute
	max := 5 * time.Minute

	t.Run("override wins", func(t *testing.T) {
		msg := fluxmsg.New()
		msg.Metadata[fluxmsg.MetaCoatCheckTTL] = "30s"
		assert.Equal(t, 30*time.Second, g.resolveTTL(msg, def, max))
	})

	t.Run("no override uses default", func(t *testing.T) {
		assert.Equal(t, def, g.resolveTTL(fluxmsg.New(), def, max))
	})

	t.Run("nil message uses default", func(t *testing.T) {
		assert.Equal(t, def, g.resolveTTL(nil, def, max))
	})

	t.Run("override above max is capped", func(t *testing.T) {
		msg := fluxmsg.New()
		msg.Metadata[fluxmsg.MetaCoatCheckTTL] = "1h"
		assert.Equal(t, max, g.resolveTTL(msg, def, max))
	})

	t.Run("unparseable override falls back to default", func(t *testing.T) {
		msg := fluxmsg.New()
		msg.Metadata[fluxmsg.MetaCoatCheckTTL] = "not a duration"
		assert.Equal(t, def, g.resolveTTL(msg, def, max))
	})
}
