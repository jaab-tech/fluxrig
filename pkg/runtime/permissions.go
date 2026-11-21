// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/events"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
)

// Permissions are the subjects one Rack publishes to and subscribes to while
// it runs a scenario. A NATS that gives each Rack its own user limits that user
// to these, so a Rack cannot read or change what belongs to another Rack. The
// Rack also answers requests it receives, which a NATS user allows with
// allow_responses.
type Permissions struct {
	Publish   []string
	Subscribe []string
}

// PermissionSubjects are the names a Rack is configured with that its
// permissions refer to.
type PermissionSubjects struct {
	// Stream is the message stream (snake.stream_name).
	Stream string
	// Telemetry is the subject root of telemetry (telemetry.base_subject).
	Telemetry string
	// TelemetryStream is the telemetry stream (telemetry.stream_name), where a
	// Rack reads back the probes of its telemetry handshake when it is approved.
	TelemetryStream string
	// DLQ is the subject root of the dead letter queue (snake.dlq_subject).
	// Empty when the Rack does not use one.
	DLQ string
}

// ManifestLookup returns the manifest of a gear type.
type ManifestLookup func(gearType string) (sdk.Manifest, bool)

// RackPermissions returns what rack needs on NATS to run sc. A gear that names
// no Rack runs on every Rack, so its needs are in the permissions of each.
// manifest gives the bus needs of each gear type; a type it does not know adds
// only what every gear needs.
func RackPermissions(sc *registry.Scenario, rack string, names PermissionSubjects, manifest ManifestLookup) Permissions {
	pub, sub := map[string]bool{}, map[string]bool{}
	api := func(format string, a ...any) { pub["$JS.API."+fmt.Sprintf(format, a...)] = true }
	stream := names.Stream

	// What every Rack does: enroll, report, emit its own messages and events,
	// and read its scenario and notifications.
	pub[fluxmsg.SubjectAgentHello] = true
	pub[fluxmsg.SubjectAgentHeartbeat] = true
	pub[fmt.Sprintf("flux.msg.%s.>", rack)] = true
	pub[events.ConnSubject(rack, ">")] = true
	pub[strings.TrimSuffix(names.Telemetry, ".")+".>"] = true
	if names.DLQ != "" {
		pub[strings.TrimSuffix(names.DLQ, ".")+".>"] = true
	}
	api("INFO")
	api("STREAM.INFO.%s", stream)
	token := fluxmsg.SubjectToken(rack)
	api("CONSUMER.CREATE.%s.*.flux.agent.enrollment.%s.*", stream, token)
	// The machine ID after the name is not known before the Rack enrolls.
	api("CONSUMER.CREATE.%s.*.%s", stream, strings.TrimSuffix(fluxmsg.NotifySubject(rack, uuid.Nil), uuid.Nil.String())+"*")
	api("CONSUMER.CREATE.%s.*.%s%s.>", stream, fluxmsg.SubjectScenarioPrefix, rack)
	// Ephemeral consumers are named at random: a Rack pulls and acks those it
	// created, and cannot list the others. It also looks one up when it misses
	// heartbeats, to tell a consumer that is gone from a slow link: refused,
	// the lookup only timed out, and a lost consumer was replaced late.
	api("CONSUMER.MSG.NEXT.%s.*", stream)
	api("CONSUMER.INFO.%s.*", stream)
	pub[fmt.Sprintf("$JS.ACK.%s.>", stream)] = true
	sub[bus.RackInboxPrefix(rack)+".>"] = true
	if ts := names.TelemetryStream; ts != "" {
		api("STREAM.INFO.%s", ts)
		api("CONSUMER.CREATE.%s.*.%s", ts, telemetry.HandshakeSubject(rack))
		api("CONSUMER.MSG.NEXT.%s.*", ts)
		api("CONSUMER.INFO.%s.*", ts)
		pub[fmt.Sprintf("$JS.ACK.%s.>", ts)] = true
	}

	deploy := deployMap(sc)
	terminusOf := func(gearType string) sdk.TerminusKind {
		m, _ := manifest(gearType)
		return m.Terminus
	}
	bindings := computeBindings(sc, rack, deploy, terminusOf)
	for _, g := range sc.Gears {
		if target, _ := g.Deploy.(string); target != "" && target != rack {
			continue
		}
		// Commands to the gear, and the link state it publishes and follows.
		sub[fmt.Sprintf("flux.ctrl.%s", g.Name)] = true
		pub[fmt.Sprintf("flux.ctrl.link.%s", g.Name)] = true
		for _, b := range bindings[g.Name] {
			if b.Kind == sdk.BindingIO {
				sub[fmt.Sprintf("flux.ctrl.link.%s", b.Gear)] = true
			}
		}
		m, ok := manifest(g.Type)
		if !ok || m.BusNeeds == nil {
			continue
		}
		for _, need := range m.BusNeeds(g.Config) {
			addBusNeed(api, pub, need, rack, g.Name, deploy)
		}
	}

	for subject := range busConsumeSubjects(sc, rack, deploy) {
		api("CONSUMER.CREATE.%s.*.%s", stream, subject)
		// Before its gears start, the Rack publishes a sync probe on each
		// subject it consumes and waits to read it back (waitForConvergence),
		// so a wire from another Rack needs that Rack's subject too. Every
		// other consumer of the subject skips a probe.
		pub[subject] = true
	}
	return Permissions{Publish: sortedKeys(pub), Subscribe: sortedKeys(sub)}
}

// addBusNeed adds what one need of gear on rack takes.
func addBusNeed(api func(string, ...any), pub map[string]bool, need sdk.BusNeed, rack, gear string, deploy map[string]string) {
	switch need.Kind {
	case sdk.BusReadEvents:
		// The source resolves to a Rack the way the reader does.
		sourceRack, source := "", need.Source
		if before, after, dotted := strings.Cut(need.Source, "."); dotted {
			sourceRack, source = before, after
		}
		sourceRack = firstNonEmpty(sourceRack, deploy[source], rack)
		durable := events.DurableName(rack, gear, source)
		api("CONSUMER.CREATE.%s.%s.%s", events.StreamName, durable, events.ConnSubject(sourceRack, source))
		api("CONSUMER.INFO.%s.%s", events.StreamName, durable)
		api("CONSUMER.MSG.NEXT.%s.%s", events.StreamName, durable)
		pub[fmt.Sprintf("$JS.ACK.%s.%s.>", events.StreamName, durable)] = true
	case sdk.BusReadBucket, sdk.BusWriteBucket, sdk.BusOwnBucket:
		kv := "KV_" + need.Bucket
		api("STREAM.INFO.%s", kv)
		api("DIRECT.GET.%s", kv)
		api("DIRECT.GET.%s.>", kv)
		api("STREAM.MSG.GET.%s", kv)
		if need.Kind == sdk.BusReadBucket {
			return
		}
		pub[fmt.Sprintf("$KV.%s.>", need.Bucket)] = true
		if need.Kind == sdk.BusWriteBucket {
			return
		}
		api("STREAM.CREATE.%s", kv)
		api("STREAM.UPDATE.%s", kv)
		api("CONSUMER.CREATE.%s.*.$KV.%s.>", kv, need.Bucket)
		api("CONSUMER.MSG.NEXT.%s.*", kv)
		api("CONSUMER.INFO.%s.*", kv)
		pub[fmt.Sprintf("$JS.ACK.%s.>", kv)] = true
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
