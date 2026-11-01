// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bento

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/warpstreamlabs/bento/public/service"

	"github.com/jaab-tech/fluxrig/pkg/events"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// eventSource is one entry of ports.events.
type eventSource struct {
	rack, gear string
	config     events.ReaderConfig
}

// parseEventSources reads ports.events. A gear resolves to a Rack the way the
// source of a wire does: an explicit rack.gear, or the Rack the gear is deployed
// to, or this Rack.
func parseEventSources(ctx sdk.GearContext, entries []any) ([]eventSource, error) {
	provider, ok := ctx.(sdk.EventProvider)
	if !ok {
		return nil, errors.New("this context cannot read events")
	}
	sources := make([]eventSource, 0, len(entries))
	for i, entry := range entries {
		raw, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d is not an object", i)
		}
		ref, _ := raw["gear"].(string)
		if ref == "" {
			return nil, fmt.Errorf("entry %d has no gear", i)
		}
		rack, gear := "", ref
		if before, after, dotted := strings.Cut(ref, "."); dotted {
			rack, gear = before, after
		}
		if rack == "" {
			rack = provider.GearRack(gear)
		}

		cfg := events.ReaderConfig{
			Subject: events.ConnSubject(rack, gear),
			Durable: events.DurableName(provider.RackName(), ctx.GearName(), gear),
			Logger:  ctx.Logger(),
			Conn: func() *nats.Conn {
				conn, _ := ctx.Bus().Core().(*nats.Conn)
				return conn
			},
		}
		var err error
		if cfg.MaxEventAge, err = entryDuration(raw, "max_event_age", i); err != nil {
			return nil, err
		}
		if cfg.InactiveAfter, err = entryDuration(raw, "consumer_inactive_after", i); err != nil {
			return nil, err
		}
		if on, ok := raw["on"].([]any); ok {
			for _, name := range on {
				s, _ := name.(string)
				cfg.On = append(cfg.On, s)
			}
		}
		// A count arrives as int from YAML, and as float64 from JSON.
		switch v := raw["max_deliver"].(type) {
		case int:
			cfg.MaxDeliver = v
		case float64:
			cfg.MaxDeliver = int(v)
		}
		sources = append(sources, eventSource{rack: rack, gear: gear, config: cfg})
	}
	return sources, nil
}

func entryDuration(raw map[string]any, key string, index int) (time.Duration, error) {
	s, ok := raw[key].(string)
	if !ok {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("entry %d: %s: %w", index, key, err)
	}
	return d, nil
}

// registerEventInput registers the input type of this gear's events.
func (g *Gear) registerEventInput() error {
	spec := service.NewConfigSpec().
		Summary("Reads durable events for a fluxrig gear.").
		Field(service.NewIntField("source").Default(0))
	return g.env.RegisterInput(g.eventType, spec,
		func(conf *service.ParsedConfig, _ *service.Resources) (service.Input, error) {
			index, err := conf.FieldInt("source")
			if err != nil {
				return nil, err
			}
			if index < 0 || index >= len(g.eventSources) {
				return nil, fmt.Errorf("no event source %d", index)
			}
			return &fluxEventInput{g: g, reader: events.NewReader(g.eventSources[index].config)}, nil
		})
}

// eventInputConfig builds the input of a gear with ports.events: one source
// alone, or a broker with each source and the declared input port.
func (g *Gear) eventInputConfig(inputs []string) map[string]any {
	sources := make([]any, 0, len(g.eventSources)+1)
	for i := range g.eventSources {
		sources = append(sources, map[string]any{g.eventType: map[string]any{"source": i}})
	}
	if len(inputs) > 0 {
		sources = append(sources, map[string]any{g.inputType: map[string]any{"name": inputs[0]}})
	}
	if len(sources) == 1 {
		return sources[0].(map[string]any)
	}
	return map[string]any{"broker": map[string]any{"inputs": sources}}
}

// fluxEventInput gives one Bento message for each event. The ack of the message
// is the ack of the event, so it comes when the pipeline ends.
type fluxEventInput struct {
	g      *Gear
	reader *events.Reader
}

func (i *fluxEventInput) Connect(ctx context.Context) error {
	err := i.reader.Open(ctx)
	if errors.Is(err, events.ErrNotConnected) {
		return service.ErrNotConnected
	}
	return err
}

func (i *fluxEventInput) Read(ctx context.Context) (*service.Message, service.AckFunc, error) {
	d, err := i.reader.Next(ctx)
	switch {
	case errors.Is(err, events.ErrNotConnected):
		return nil, nil, service.ErrNotConnected
	case errors.Is(err, context.Canceled):
		return nil, nil, service.ErrEndOfInput
	case err != nil:
		return nil, nil, err
	}

	m := service.NewMessage(d.Data)
	if id, errID := i.g.gearCtx.IDGen().NextFluxID(); errID == nil {
		m.MetaSet("flux_id", id.String())
	}
	m.MetaSet("conn.id", d.Event.ConnID)
	m.MetaSet("conn.event", d.Event.Event)
	m.MetaSet("conn.gear", d.Event.Gear)
	m.MetaSet("conn.rack", d.Event.Rack)
	m.MetaSet("peer.ip", d.Event.PeerIP)
	m.MetaSet("peer.port", d.Event.PeerPort)
	if d.Event.Reason != "" {
		m.MetaSet("conn.reason", d.Event.Reason)
	}
	return m, func(_ context.Context, ackErr error) error { return d.Ack(ackErr) }, nil
}

func (i *fluxEventInput) Close(_ context.Context) error {
	i.reader.Close()
	return nil
}
