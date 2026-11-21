// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/runtime"
)

// The users the command writes load into a NATS server as they are, each
// with the permissions of its own Rack.
func TestWritePermissions_LoadsIntoNATS(t *testing.T) {
	sc := &registry.Scenario{
		Meta: registry.ScenarioMeta{Name: "two", Version: "1.0.0"},
		Gears: []registry.GearSpec{
			{Name: "src", Type: "io_iso8583", Deploy: "rack-a", Config: map[string]any{"mode": "server"}},
			{Name: "dst", Type: "io_iso8583", Deploy: "rack-b", Config: map[string]any{"mode": "client"}},
		},
		Wires: []registry.WireSpec{{From: "src.out", To: "dst.in"}},
	}
	racks, err := permissionRacks([]*registry.Scenario{sc}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"rack-a", "rack-b"}, racks)

	var out bytes.Buffer
	names := runtime.PermissionSubjects{Stream: "flux-msg", Telemetry: "flux.telemetry"}
	require.NoError(t, writePermissions(&out, []*registry.Scenario{sc}, racks, "CN={rack},O=test", names, gears.NewFactory().Manifest))

	conf := filepath.Join(t.TempDir(), "n.conf")
	require.NoError(t, os.WriteFile(conf, []byte("authorization { users: [\n"+out.String()+"] }\n"), 0o600))
	opts, err := server.ProcessConfigFile(conf)
	require.NoError(t, err, out.String())
	require.Len(t, opts.Users, 2)
	for _, u := range opts.Users {
		require.NotNil(t, u.Permissions, u.Username)
		assert.True(t, u.Permissions.Response != nil, "%s answers requests", u.Username)
		if u.Username == "CN=rack-b,O=test" {
			assert.Contains(t, u.Permissions.Publish.Allow, "$JS.API.CONSUMER.CREATE.flux-msg.*.flux.msg.rack-a.src.out")
		}
	}
}

func TestPermissionRacks_GearOnEveryRackNeedsRacks(t *testing.T) {
	sc := &registry.Scenario{Gears: []registry.GearSpec{{Name: "g", Type: "bento"}}}
	_, err := permissionRacks([]*registry.Scenario{sc}, nil)
	assert.ErrorContains(t, err, "--rack")
	racks, err := permissionRacks([]*registry.Scenario{sc}, []string{"r1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"r1"}, racks)
}

// With two scenarios, a Rack can run either one: it gets what both need.
func TestUnionPermissions_TwoScenarios(t *testing.T) {
	a := &registry.Scenario{Gears: []registry.GearSpec{{Name: "a", Type: "coatcheck", Deploy: "r1", Config: map[string]any{"mode": "store", "bucket": "one"}}}}
	b := &registry.Scenario{Gears: []registry.GearSpec{{Name: "b", Type: "coatcheck", Deploy: "r1", Config: map[string]any{"mode": "store", "bucket": "two"}}}}
	p := unionPermissions([]*registry.Scenario{a, b}, "r1", runtime.PermissionSubjects{Stream: "flux-msg", Telemetry: "flux.telemetry"}, gears.NewFactory().Manifest)
	assert.Contains(t, p.Publish, "$KV.one.>")
	assert.Contains(t, p.Publish, "$KV.two.>")
	assert.Contains(t, p.Subscribe, "flux.ctrl.a")
	assert.Contains(t, p.Subscribe, "flux.ctrl.b")
}
