// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaab-tech/fluxrig/pkg/gears"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Two Racks: rack-a accepts connections and parks context in a bucket; rack-b
// reads the connection events of rack-a's server and consumes its output over
// a wire between the Racks.
func permissionScenario() *registry.Scenario {
	return &registry.Scenario{
		Gears: []registry.GearSpec{
			{Name: "server_io", Type: "io_iso8583", Deploy: "rack-a", Config: map[string]any{"mode": "server", "bind": ":8888"}},
			{Name: "cc_daemon", Type: "coatcheck", Deploy: "rack-a", Config: map[string]any{"mode": "daemon", "bucket": "ctx"}},
			{Name: "cc_store", Type: "coatcheck", Deploy: "rack-a", Config: map[string]any{"mode": "store", "bucket": "ctx"}},
			{Name: "on_connect", Type: "bento", Deploy: "rack-b", Config: map[string]any{
				"ports": map[string]any{"events": []any{map[string]any{"gear": "server_io", "on": []any{"opened"}}}},
			}},
			{Name: "module", Type: "wasm", Deploy: "rack-b", Config: map[string]any{"source": "snake://modules/m.wasm"}},
		},
		Wires: []registry.WireSpec{
			{From: "server_io.out", To: "cc_store.in"},
			{From: "cc_store.out", To: "module.in"},
		},
	}
}

func TestRackPermissions_EachRackGetsItsOwn(t *testing.T) {
	sc := permissionScenario()
	names := PermissionSubjects{Stream: "flux-msg", Telemetry: "flux.telemetry", TelemetryStream: "flux-telemetry", DLQ: "flux.dlq"}
	f := gears.NewFactory()
	a := RackPermissions(sc, "rack-a", names, f.Manifest)
	b := RackPermissions(sc, "rack-b", names, f.Manifest)

	// rack-a owns the bucket its gears use.
	for _, s := range []string{"$JS.API.STREAM.CREATE.KV_ctx", "$KV.ctx.>", "$JS.API.CONSUMER.CREATE.KV_ctx.*.$KV.ctx.>"} {
		assert.Contains(t, a.Publish, s)
		assert.NotContains(t, b.Publish, s)
	}
	// rack-b reads rack-a's server events through its own durable reader, and
	// consumes the wire from rack-a.
	for _, s := range []string{
		"$JS.API.CONSUMER.CREATE.flux-events.rack-b_on_connect_server_io.flux.evt.conn.rack-a.server_io",
		"$JS.API.CONSUMER.MSG.NEXT.flux-events.rack-b_on_connect_server_io",
		"$JS.API.CONSUMER.CREATE.flux-msg.*.flux.msg.rack-a.cc_store.out",
		// the sync probe on the wire it consumes
		"flux.msg.rack-a.cc_store.out",
		"$JS.API.STREAM.INFO.KV_modules",
	} {
		assert.Contains(t, b.Publish, s)
		assert.NotContains(t, a.Publish, s)
	}
	assert.NotContains(t, b.Publish, "$KV.modules.>", "a module bucket is read, not written")

	// Each Rack publishes and receives only under its own name, and commands
	// reach only the gears deployed to it.
	assert.Contains(t, a.Publish, "flux.msg.rack-a.>")
	assert.NotContains(t, a.Publish, "flux.msg.rack-b.>")
	assert.Equal(t, []string{"_INBOX_rack-a.>"}, filter(a.Subscribe, "_INBOX"))
	assert.Contains(t, a.Subscribe, "flux.ctrl.server_io")
	assert.NotContains(t, a.Subscribe, "flux.ctrl.on_connect")

	// The telemetry handshake reads back only this Rack's own probes.
	assert.Contains(t, a.Publish, "$JS.API.CONSUMER.CREATE.flux-telemetry.*.flux.telemetry.rack-a.logs")
	assert.NotContains(t, a.Publish, "$JS.API.CONSUMER.CREATE.flux-telemetry.*.flux.telemetry.rack-b.logs")
	assert.Contains(t, a.Publish, "$JS.API.CONSUMER.CREATE.flux-msg.*.flux.agent.notify.rack-a.*")
	// A missed heartbeat makes the Rack look its consumers up.
	assert.Contains(t, a.Publish, "$JS.API.CONSUMER.INFO.flux-msg.*")

	// Nothing reaches the whole API, a whole stream or another Rack's inbox.
	for _, p := range [][]string{a.Publish, b.Publish, a.Subscribe, b.Subscribe} {
		for _, s := range p {
			assert.NotContains(t, []string{">", "$JS.API.>", "_INBOX.>", "flux.>", "flux.msg.>"}, s)
			assert.False(t, strings.Contains(s, ".DELETE.") || strings.Contains(s, ".PURGE."), s)
		}
	}
}

// A gear that names no Rack runs on every Rack, so each Rack holds its needs.
func TestRackPermissions_GearOnEveryRack(t *testing.T) {
	sc := &registry.Scenario{Gears: []registry.GearSpec{
		{Name: "cc", Type: "coatcheck", Config: map[string]any{"mode": "store", "bucket": "shared"}},
	}}
	f := gears.NewFactory()
	for _, rack := range []string{"rack-a", "rack-b"} {
		p := RackPermissions(sc, rack, PermissionSubjects{Stream: "flux-msg", Telemetry: "flux.telemetry"}, f.Manifest)
		assert.Contains(t, p.Publish, "$KV.shared.>", rack)
		assert.Empty(t, filter(p.Publish, "flux.dlq"), "no dead letter subject when the Rack has none")
	}
}

func filter(subjects []string, prefix string) []string {
	return slices.DeleteFunc(slices.Clone(subjects), func(s string) bool { return !strings.HasPrefix(s, prefix) })
}
