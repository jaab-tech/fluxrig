// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/runtime"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// subjectMatches reports whether subject is delivered to a subscription on
// pattern, with NATS wildcard semantics: "*" takes one token, ">" the rest.
func subjectMatches(pattern, subject string) bool {
	pt, st := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, p := range pt {
		switch {
		case p == ">":
			return true
		case i >= len(st):
			return false
		case p == "*":
			continue
		case p != st[i]:
			return false
		}
	}
	return len(pt) == len(st)
}

// The forward plane answers any NATS publisher: Request carries no identity
// or credentials, and the only fence is the subject authorization the
// operator gives each NATS user (the Mixers are unrestricted, each Rack gets
// what `fluxrig scenario permissions` writes). The probe asserts on a live
// cluster that a Rack user cannot publish or subscribe on
// flux.mixer.control.>; this test pins the other half of that assumption in
// the generator itself: no subject a Rack is granted may match a forwarded
// control subject. Either side moving — the prefix, an op name, or the
// permissions — breaks this test before a Rack can reach the holder.
func TestForwardSubjectsAreOutsideRackPermissions(t *testing.T) {
	sc := &registry.Scenario{Gears: []registry.GearSpec{
		{Name: "g1", Type: "unknown-type", Deploy: "rack-a"},
	}}
	// An unknown gear type grants only what every gear needs: the subject
	// list below is then the widest the stock permissions reach.
	perms := runtime.RackPermissions(sc, "rack-a",
		runtime.PermissionSubjects{Stream: "flux-msg", Telemetry: "flux.telemetry"},
		func(string) (sdk.Manifest, bool) { return sdk.Manifest{}, false })

	granted := append(append([]string{}, perms.Publish...), perms.Subscribe...)
	assert.NotEmpty(t, granted, "a Rack with a gear must be granted something to compare against")

	ops := []string{
		OpRegisterEntity, OpApprove, OpRemoveEntity, OpRemoveByName,
		OpUpdateStatusEntity, OpPutScenario, OpSetActive, OpImportSpec,
	}
	for _, op := range ops {
		subject := SubjectPrefix + op
		for _, pattern := range granted {
			assert.False(t, subjectMatches(pattern, subject),
				"forwarded op %q (%s) is reachable through the Rack permission %q", op, subject, pattern)
		}
	}
}

// The matcher above is the assertion's hinge, so it is tested on its own:
// a mistake in it would pass the test above while a Rack reaches the holder.
func TestSubjectMatches(t *testing.T) {
	for pattern, subjects := range map[string][]string{
		"flux.mixer.control.>": {"flux.mixer.control.approve"},
		"flux.ctrl.*":          {"flux.ctrl.g1"},
		"*":                    {"flux"},
	} {
		for _, s := range subjects {
			assert.True(t, subjectMatches(pattern, s), pattern+" matches "+s)
		}
	}
	for pattern, subjects := range map[string][]string{
		"flux.mixer.control.>": {"flux.mixer.approve", "other.mixer.control.approve"},
		"flux.ctrl.g1":         {"flux.ctrl.g1.extra"},
		"flux.ctrl.>":          {"flux.mixer.control.approve"},
	} {
		for _, s := range subjects {
			assert.False(t, subjectMatches(pattern, s), pattern+" must not match "+s)
		}
	}
}
