// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package fluxmsg

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// Rack names become NATS subject tokens (enrollment topics, scenario pushes,
// notify subjects). An adversarial name must never survive as a separator or
// a wildcard: both Mixer and Rack derive the same subject from it.
func TestSubjectToken_AdversarialNames(t *testing.T) {
	for _, name := range []string{"..", "...", "*", ">", "", ".", "#", "a.b", "rack name", "../x", "a/b", "a*b", "a>b", "-", "_", "RACK-01_ok"} {
		token := SubjectToken(name)
		assert.NotEmpty(t, token, "name %q", name)
		assert.NotContains(t, token, ".", "name %q: no separator survives", name)
		assert.NotContains(t, token, "*", "name %q: no wildcard survives", name)
		assert.NotContains(t, token, ">", "name %q: no wildcard survives", name)
		assert.NotContains(t, token, " ", "name %q: no space survives", name)
		assert.NotContains(t, token, "/", "name %q: no path separator survives", name)
	}

	assert.Equal(t, "unnamed", SubjectToken(""), "empty has a fallback")
	assert.Equal(t, "unnamed", SubjectToken(".."), "dots alone have a fallback")
	assert.Equal(t, "unnamed", SubjectToken("*"), "a lone wildcard has a fallback")
	assert.Equal(t, "a_b", SubjectToken("a.b"), "a separator inside a name is contained")

	// The notify subject of an adversarial name carries no wildcard and no
	// empty token: a NATS user scoped to one Rack cannot escape it.
	for _, name := range []string{"..", "*", ">", "a.b", ""} {
		subject := NotifySubject(name, uuid.New())
		parts := strings.Split(subject, ".")
		for _, p := range parts {
			assert.NotEmpty(t, p, "subject %q has no empty token", subject)
			assert.NotContains(t, p, "*", "subject %q", subject)
			assert.NotContains(t, p, ">", "subject %q", subject)
		}
	}
}
