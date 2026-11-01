// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/events"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/idgen"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/snake"
)

// rackContext is a gear context on a Rack: it can read events.
type rackContext struct {
	MockContext
	name    string
	rack    string
	deploy  map[string]string
	gen     sdk.IDGenerator
	b       bus.Bus
	emitter sdk.PortEmitter
}

func (c *rackContext) GearName() string       { return c.name }
func (c *rackContext) IDGen() sdk.IDGenerator { return c.gen }
func (c *rackContext) Bus() bus.Bus           { return c.b }
func (c *rackContext) Emitter() sdk.PortEmitter {
	if c.emitter != nil {
		return c.emitter
	}
	return sdk.NewNoopEmitter()
}
func (c *rackContext) RackName() string { return c.rack }
func (c *rackContext) GearRack(gear string) string {
	if r := c.deploy[gear]; r != "" {
		return r
	}
	return c.rack
}
func (c *rackContext) PublishEvent(context.Context, string, []byte, uuid.UUID) error { return nil }

func eventGearConfig(entries ...any) map[string]any {
	return map[string]any{
		"ports": map[string]any{"outputs": []any{"out"}, "events": entries},
		"bento": map[string]any{"pipeline": map[string]any{"processors": []any{
			map[string]any{"mapping": "root = this"},
		}}},
	}
}

func newRackContext(name string, config map[string]any) *rackContext {
	return &rackContext{MockContext: MockContext{config: config}, name: name, rack: "rack1"}
}

func TestEventSources_ResolveLikeAWireSource(t *testing.T) {
	ctx := newRackContext("on_connect", nil)
	ctx.deploy = map[string]string{"pinned": "rack9"}
	sources, err := parseEventSources(ctx, []any{
		map[string]any{"gear": "server_io"},
		map[string]any{"gear": "pinned"},
		map[string]any{"gear": "rack7.server_io"},
	})
	require.NoError(t, err)
	assert.Equal(t, "flux.evt.conn.rack1.server_io", sources[0].config.Subject, "a gear on every Rack reads from this Rack")
	assert.Equal(t, "flux.evt.conn.rack9.pinned", sources[1].config.Subject)
	assert.Equal(t, "flux.evt.conn.rack7.server_io", sources[2].config.Subject)
	assert.Equal(t, "rack1_on_connect_server_io", sources[0].config.Durable)
}

// The same scenario on two Racks must give two consumers: one shared consumer
// would hand each event to one Rack, right or wrong.
func TestEventSources_EachRackHasItsOwnConsumer(t *testing.T) {
	one := newRackContext("on_connect", nil)
	two := newRackContext("on_connect", nil)
	two.rack = "rack2"
	entry := []any{map[string]any{"gear": "server_io"}}
	a, err := parseEventSources(one, entry)
	require.NoError(t, err)
	b, err := parseEventSources(two, entry)
	require.NoError(t, err)
	assert.NotEqual(t, a[0].config.Durable, b[0].config.Durable)
	assert.NotEqual(t, a[0].config.Subject, b[0].config.Subject)
}

func TestEventSources_ReadTheLimits(t *testing.T) {
	sources, err := parseEventSources(newRackContext("r", nil), []any{map[string]any{
		"gear": "g", "on": []any{"opened"}, "max_event_age": "10s", "max_deliver": 3, "consumer_inactive_after": "2h",
	}})
	require.NoError(t, err)
	cfg := sources[0].config
	assert.Equal(t, []string{"opened"}, cfg.On)
	assert.Equal(t, 10*time.Second, cfg.MaxEventAge)
	assert.Equal(t, 3, cfg.MaxDeliver)
	assert.Equal(t, 2*time.Hour, cfg.InactiveAfter)
}

func TestEventSources_NeedARack(t *testing.T) {
	g := New()
	err := g.Init(&MockContext{config: eventGearConfig(map[string]any{"gear": "server_io"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ports.events")
}

func TestEventInput_RefusesAnInputOfItsOwn(t *testing.T) {
	config := eventGearConfig(map[string]any{"gear": "server_io"})
	config["bento"].(map[string]any)["input"] = map[string]any{"generate": map[string]any{"mapping": "root = {}"}}
	err := New().Init(newRackContext("own_input", config))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ports.events")
}

// A buffer would ack the event on arrival, before the pipeline ends.
func TestEventInput_HasNoBuffer(t *testing.T) {
	config := eventGearConfig(map[string]any{"gear": "server_io"})
	g := New()
	require.NoError(t, g.Init(newRackContext("no_buffer", config)))
	_, hasBuffer := g.config.Bento["buffer"]
	assert.False(t, hasBuffer)
	assert.Contains(t, g.config.Bento["input"], g.eventType)
}

func TestEventInput_SeveralSourcesUseABroker(t *testing.T) {
	config := eventGearConfig(map[string]any{"gear": "a"}, map[string]any{"gear": "b"})
	config["ports"].(map[string]any)["inputs"] = []any{"in"}
	g := New()
	require.NoError(t, g.Init(newRackContext("broker", config)))
	broker := g.config.Bento["input"].(map[string]any)["broker"].(map[string]any)
	assert.Len(t, broker["inputs"], 3)
}

// recordingEmitter fails its first failures emissions, then records.
type recordingEmitter struct {
	mu       sync.Mutex
	failures int
	attempts int
	got      []*fluxmsg.FluxMsg
}

func (e *recordingEmitter) Emit(_ string, msg *fluxmsg.FluxMsg) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts++
	if e.attempts <= e.failures {
		return errors.New("emission failed")
	}
	e.got = append(e.got, msg)
	return nil
}

func (e *recordingEmitter) messages() []*fluxmsg.FluxMsg {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*fluxmsg.FluxMsg(nil), e.got...)
}

func (e *recordingEmitter) tries() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts
}

// coreBus is a bus that only offers its connection, as a reader needs.
type coreBus struct {
	*bus.MockBus
	conn *nats.Conn
}

func (b *coreBus) Core() any { return b.conn }

func startEventStream(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	s, err := snake.NewServer(context.Background(), snake.Config{Port: -1, ClusterName: t.Name(), StoreDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(context.Background(), jetstream.StreamConfig{Name: events.StreamName, Subjects: []string{events.SubjectRoot + ".>"}})
	require.NoError(t, err)
	return nc, js
}

func startEventGear(t *testing.T, name string, nc *nats.Conn, emitter sdk.PortEmitter, config map[string]any) *Gear {
	t.Helper()
	gen, err := idgen.New(uuid.New())
	require.NoError(t, err)
	ctx := newRackContext(name, config)
	ctx.gen, ctx.b, ctx.emitter = gen, &coreBus{MockBus: bus.NewMockBus(), conn: nc}, emitter
	g := New()
	require.NoError(t, g.Init(ctx))
	require.NoError(t, g.Start(context.Background(), func(*fluxmsg.FluxMsg) {}))
	t.Cleanup(func() { _ = g.Stop() })
	return g
}

func publishEvent(t *testing.T, js jetstream.JetStream, ev events.ConnEvent) {
	t.Helper()
	data, err := json.Marshal(ev)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), events.ConnSubject("rack1", "server_io"), data)
	require.NoError(t, err)
}

func TestEventInput_GivesAMessageThatReachesTheConnection(t *testing.T) {
	nc, js := startEventStream(t)
	emitter := &recordingEmitter{}
	config := eventGearConfig(map[string]any{"gear": "server_io", "on": []any{"opened"}})
	config["bento"].(map[string]any)["pipeline"] = map[string]any{"processors": []any{
		map[string]any{"mapping": `meta "iso8583.mti" = "0800"
root.iso8583.field."70" = "001"`},
	}}
	startEventGear(t, "logon", nc, emitter, config)

	publishEvent(t, js, events.ConnEvent{Event: events.Opened, ConnID: "0xabc", Gear: "server_io", Rack: "rack1", PeerIP: "10.0.0.1", PeerPort: "4000", TS: time.Now().UTC()})

	require.Eventually(t, func() bool { return len(emitter.messages()) == 1 }, 10*time.Second, 10*time.Millisecond)
	msg := emitter.messages()[0]
	assert.Equal(t, "0xabc", msg.Metadata["conn.id"], "this is what io_iso8583 routes a reply by")
	assert.Equal(t, "opened", msg.Metadata["conn.event"])
	assert.Equal(t, "server_io", msg.Metadata["conn.gear"])
	assert.Equal(t, "10.0.0.1", msg.Metadata["peer.ip"])
	assert.Equal(t, "4000", msg.Metadata["peer.port"])
	assert.Equal(t, "0800", msg.Metadata["iso8583.mti"])
	assert.NotEqual(t, uuid.Nil, msg.FluxID, "an event gives a message with its own id")
}

// The ack comes when the pipeline ends: a failed emission delivers the event
// again, and a buffer in between would have acked it already.
func TestEventInput_FailedEmissionDeliversTheEventAgain(t *testing.T) {
	nc, js := startEventStream(t)
	emitter := &recordingEmitter{failures: 1}
	startEventGear(t, "retry", nc, emitter, eventGearConfig(map[string]any{"gear": "server_io"}))

	publishEvent(t, js, events.ConnEvent{Event: events.Opened, ConnID: "0xabc", TS: time.Now().UTC()})

	require.Eventually(t, func() bool { return len(emitter.messages()) == 1 }, 15*time.Second, 10*time.Millisecond,
		"attempts: %d", emitter.tries())
	assert.Equal(t, 2, emitter.tries())
}
