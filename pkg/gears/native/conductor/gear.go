// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package conductor implements the transaction switching gear: it routes each
// structured request across named output ports via a composable destination
// tree, correlates the reply through the valet engine, and surfaces every
// abnormal outcome on the error port without ever authoring a message itself.
//
// Ports (all wired in the scenario):
//
//	in           requests to route
//	in_reply     structured replies to match
//	out_<name>   one per destination (uplink leg or peer conductor)
//	out_response matched, restored responses toward the origin
//	error        abnormal outcomes: the ORIGINAL message annotated with
//	             error.reason (no_route | no_destination | no_key | timeout |
//	             unmatched_reply); a downstream gear authors any decline.
//
// Port names use underscores, never dots: a dot in a wire endpoint separates
// rack / gear / port levels (ADR 0043), so a port name must not contain one.
package conductor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/ctrl"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/valet"
)

// error.reason values emitted on the error port.
const (
	reasonNoRoute        = "no_route"
	reasonNoDestination  = "no_destination"
	reasonNoKey          = "no_key"
	reasonTimeout        = "timeout"
	reasonUnmatchedReply = "unmatched_reply"
)

const (
	portResponse = "out_response"
	portError    = "error"

	// metaOrigin is the in-band origin stamp: a conductor with a configured
	// origin marks every routed request with its own name, so a peer that
	// serves the request knows to return the finished response on
	// out_response_<origin>.
	metaOrigin = "conductor.origin"
)

// keySep joins correlation-key parts unambiguously (unit separator, a byte
// that cannot appear in structured field values of the supported dialects).
const keySep = "\x1f"

// maxConcurrentExpiryEmits caps how many timed-out tickets may be published on
// the error port at once. valet fires each expiry on its own timer goroutine,
// and the emit is a blocking bus publish; a scheme outage can time out every
// in-flight ticket at nearly the same instant. Without a cap that becomes a
// burst of concurrent blocking publishes. Excess expiry goroutines park on the
// semaphore (cheap) instead of all hitting the bus together.
const maxConcurrentExpiryEmits = 32

// Gear is the conductor. It implements sdk.PortedGear: requests arrive on
// "in", replies on "in_reply", and every result leaves through the emitter.
type Gear struct {
	logger  *slog.Logger
	emitter sdk.PortEmitter

	origin     string
	corrKey    []string
	parkFields []string
	routes     []*route
	engine     *valet.Engine

	// availability reports whether an output port may receive traffic.
	// Ports whose terminus is a local I/O gear follow that gear's link-state
	// (down until the socket reports up); everything else falls back to the
	// base predicate (accept-all, or a test override set before Init).
	availability func(port string) bool

	// linkStates tracks socket state per output port bound to a local I/O
	// gear. Written only during Init; values flip atomically afterwards.
	linkStates map[string]*atomic.Bool
	linkSubs   []linkSub

	// clock is injected into the valet engine when set before Init (tests).
	clock valet.Clock

	// expireSem bounds concurrent error-port emissions from expiry callbacks.
	expireSem chan struct{}
}

// linkSub couples a control-plane link-state subscription to the port state
// it drives.
type linkSub struct {
	port string
	gear string
	ch   <-chan ctrl.Command
	st   *atomic.Bool
}

// New creates an unconfigured conductor gear.
func New() *Gear { return &Gear{} }

var _ sdk.PortedGear = (*Gear)(nil)

// Init parses configuration and builds the valet engine.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.logger = ctx.Logger().With("type", "conductor")
	g.emitter = ctx.Emitter()
	g.expireSem = make(chan struct{}, maxConcurrentExpiryEmits)
	cfg := ctx.Config()

	// Optional mesh identity: enables peer response routing.
	if o, ok := cfg["origin"].(string); ok {
		g.origin = o
	}

	// Correlation key tuple (never a single wrapping counter).
	rawKey, ok := cfg["correlation_key"].([]any)
	if !ok || len(rawKey) == 0 {
		return fmt.Errorf("conductor: correlation_key (list of field ids) is required")
	}
	for _, kv := range rawKey {
		s, ok := kv.(string)
		if !ok || s == "" {
			return fmt.Errorf("conductor: correlation_key entries must be field id strings")
		}
		g.corrKey = append(g.corrKey, s)
	}

	// Optional parking overlay: fields kept off the outbound leg and restored
	// on the reply from the retained original request.
	if rawPark, ok := cfg["park_fields"].([]any); ok {
		for _, pv := range rawPark {
			s, ok := pv.(string)
			if !ok || s == "" {
				return fmt.Errorf("conductor: park_fields entries must be field id strings")
			}
			g.parkFields = append(g.parkFields, s)
		}
	}

	// Routing table.
	routes, err := parseRoutes(cfg["routes"])
	if err != nil {
		return fmt.Errorf("conductor: %w", err)
	}
	g.routes = routes

	// Valet engine (ticket store).
	vcfg := valet.Config{Clock: g.clock, Logger: g.logger, OnExpire: g.onExpire}
	if vm, ok := cfg["valet"].(map[string]any); ok {
		if store, ok := vm["store"].(string); ok && store != "" && store != "memory" {
			return fmt.Errorf("conductor: valet store %q is not implemented yet (only \"memory\")", store)
		}
		ttl, derr := parseDuration(vm, "default_ttl")
		if derr != nil {
			return fmt.Errorf("conductor: valet: %w", derr)
		}
		if ttl != 0 {
			vcfg.DefaultTTL = ttl
		}
		retain, derr := parseDuration(vm, "retain_after_close")
		if derr != nil {
			return fmt.Errorf("conductor: valet: %w", derr)
		}
		if retain != 0 {
			vcfg.RetainAfterClose = retain
		}
	}
	engine, err := valet.New(vcfg)
	if err != nil {
		return fmt.Errorf("conductor: %w", err)
	}
	g.engine = engine

	// Availability sensing: ports whose terminus is a local I/O gear (from the
	// runtime's wire-graph walk) follow that gear's link-state, published on
	// the control plane at link.<gear>. Subscriptions are opened here, during
	// Init, so no transition can be missed: every gear's Init completes before
	// any gear's Start opens a socket. Consumption begins in Start.
	base := g.availability
	if base == nil {
		base = func(string) bool { return true }
	}
	g.linkStates = make(map[string]*atomic.Bool)
	if bp, ok := ctx.(sdk.BindingsProvider); ok {
		if cp, ok := ctx.ControlPlane().(ctrl.ControlPlane); ok && cp != nil {
			for port, binding := range bp.Bindings() {
				if binding.Kind != sdk.BindingIO {
					continue
				}
				ch, err := cp.Subscribe("link." + binding.Gear)
				if err != nil {
					g.logger.Warn("link-state subscribe failed; port stays outcome-sensed",
						"port", port, "gear", binding.Gear, "error", err)
					continue
				}
				st := &atomic.Bool{} // down until the socket reports up
				g.linkStates[port] = st
				g.linkSubs = append(g.linkSubs, linkSub{port: port, gear: binding.Gear, ch: ch, st: st})
			}
		}
	}
	g.availability = func(port string) bool {
		if st, ok := g.linkStates[port]; ok {
			return st.Load()
		}
		return base(port)
	}
	return nil
}

func parseDuration(m map[string]any, key string) (time.Duration, error) {
	v, ok := m[key]
	if !ok {
		return 0, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("%s must be a duration string", key)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

// Start launches the link-state consumers (expiry timers already run inside
// the valet engine). Events published between a socket's Start and this Start
// buffer in the subscription channels opened during Init.
func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	for _, sub := range g.linkSubs {
		sub := sub
		go func() {
			for cmd := range sub.ch {
				up := cmd.Cmd == "conn.up"
				sub.st.Store(up)
				g.logger.Info("destination link state", "port", sub.port, "gear", sub.gear, "up", up)
			}
		}()
	}
	return nil
}

// Process satisfies NativeGear for callers that do not use ported delivery;
// it treats the message as a request arriving on "in".
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	return nil, g.ProcessPort(ctx, "in", msg)
}

// ProcessPort dispatches by arrival port: the port tells the gear the
// message's role, no payload inspection needed.
func (g *Gear) ProcessPort(ctx context.Context, port string, msg *fluxmsg.FluxMsg) error {
	switch port {
	case "in":
		return g.handleRequest(ctx, msg)
	case "in_reply":
		return g.handleReply(ctx, msg)
	default:
		return fmt.Errorf("conductor: unexpected input port %q", port)
	}
}

// handleRequest routes one structured request.
func (g *Gear) handleRequest(ctx context.Context, msg *fluxmsg.FluxMsg) error {
	key, err := g.buildKey(msg)
	if err != nil {
		g.emitError(msg, reasonNoKey)
		return nil
	}

	rt := g.matchRoute(msg)
	if rt == nil {
		g.emitError(msg, reasonNoRoute)
		return nil
	}

	// Snapshot in-flight counts once (one engine-lock acquisition) rather than
	// per leaf, and only when the tree actually balances by load.
	load := func(string) int { return 0 }
	if rt.needsLoad {
		counts := g.engine.InFlightSnapshot(rt.leaves)
		load = func(port string) int { return counts[port] }
	}
	dest := rt.tree.pick(selection{
		available: g.availability,
		load:      load,
	})
	if dest == "" {
		// Nothing available anywhere in the tree; no ticket is parked.
		g.emitError(msg, reasonNoDestination)
		return nil
	}

	// Park before emit so a fast reply can never race an absent ticket. The
	// parked request is an independent clone: the outbound copy is then
	// stripped and origin-stamped, and the retained request stays pristine so
	// a timeout surfaces the original message, not the mutated outbound one.
	parked := cloneMsg(msg)
	res, ticket, err := g.engine.Park(ctx, valet.ParkRequest{
		Key:         key,
		Destination: dest,
		Request:     parked,
		ReturnCtx:   returnContext(msg.Metadata),
	})
	if err != nil {
		return fmt.Errorf("conductor: park: %w", err)
	}

	switch res {
	case valet.Parked:
		g.stripParked(msg)
		// Stamp the outbound request with this conductor's mesh identity so a
		// peer serving it knows where to return the response. Local legs are
		// unaffected: metadata never crosses the external socket.
		if g.origin != "" {
			if msg.Metadata == nil {
				msg.Metadata = make(map[string]string, 1)
			}
			msg.Metadata[metaOrigin] = g.origin
		}
		return g.emitter.Emit(dest, msg)
	case valet.AttachedOpen:
		// Retransmission of an in-flight request: absorb it, the one reply
		// answers both attempts.
		g.logger.Debug("retransmission attached", "key", key, "route", rt.name)
		return nil
	case valet.ReplayedClosed:
		// Retransmission after redemption: rebuild the finished response from
		// the retained outcome (a fresh clone each time) and send it on the
		// same return path. Cloning keeps concurrent replays from sharing one
		// message and never mutates the retained outcome.
		g.logger.Debug("retained outcome replayed", "key", key, "route", rt.name)
		return g.emitter.Emit(g.responsePortFor(ticket), g.finishResponse(ticket, ticket.Outcome))
	default:
		return fmt.Errorf("conductor: unexpected park result %v", res)
	}
}

// handleReply matches one structured reply to its ticket.
func (g *Gear) handleReply(ctx context.Context, msg *fluxmsg.FluxMsg) error {
	key, err := g.buildKey(msg)
	if err != nil {
		g.emitError(msg, reasonUnmatchedReply)
		return nil
	}

	ticket, err := g.engine.Redeem(ctx, key, msg)
	if err != nil {
		if errors.Is(err, valet.ErrUnmatched) || errors.Is(err, valet.ErrAlreadyRedeemed) {
			g.emitError(msg, reasonUnmatchedReply)
			return nil
		}
		return fmt.Errorf("conductor: redeem: %w", err)
	}

	// Build the response on an independent clone: the engine retained `msg`
	// as the ticket outcome, so mutating it here would race a concurrent
	// replay reading that outcome. The clone is what leaves on the wire; the
	// retained outcome stays immutable after Redeem.
	resp := g.finishResponse(ticket, msg)
	return g.emitter.Emit(g.responsePortFor(ticket), resp)
}

// finishResponse produces the response to return home for a redeemed ticket:
// an independent clone of base with the parked fields restored and the return
// context merged in (the return context wins over reply-side transport
// metadata). Shared by the live reply path and the retained-outcome replay.
func (g *Gear) finishResponse(t *valet.Ticket, base *fluxmsg.FluxMsg) *fluxmsg.FluxMsg {
	resp := cloneMsg(base)
	g.restoreParked(t, resp)
	if len(t.ReturnCtx) > 0 {
		if resp.Metadata == nil {
			resp.Metadata = make(map[string]string, len(t.ReturnCtx))
		}
		for k, v := range t.ReturnCtx {
			resp.Metadata[k] = v
		}
	}
	return resp
}

// responsePortFor picks the return path for a redeemed ticket: requests handed
// off by another conductor (their origin stamp arrived in-band and was parked
// in the return context) go home on out_response_<origin>; everything else on
// the plain response port.
func (g *Gear) responsePortFor(t *valet.Ticket) string {
	if origin := t.ReturnCtx[metaOrigin]; origin != "" && origin != g.origin {
		return portResponse + "_" + origin
	}
	return portResponse
}

// onExpire surfaces a timed-out ticket on the error port: the original
// request, annotated, never an authored response.
func (g *Gear) onExpire(t *valet.Ticket) {
	msg := t.Request
	if msg == nil {
		g.logger.Error("ticket expired without a parked request", "key", t.Key)
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string, len(t.ReturnCtx)+1)
	}
	for k, v := range t.ReturnCtx {
		if _, exists := msg.Metadata[k]; !exists {
			msg.Metadata[k] = v
		}
	}
	msg.Metadata["error.reason"] = reasonTimeout
	// Bound how many expiry emissions hit the bus concurrently: a mass timeout
	// (e.g. a scheme outage) fires many of these callbacks at once.
	if g.expireSem != nil {
		g.expireSem <- struct{}{}
		defer func() { <-g.expireSem }()
	}
	if err := g.emitter.Emit(portError, msg); err != nil {
		g.logger.Error("timeout emission failed", "key", t.Key, "error", err)
	}
}

// buildKey extracts the correlation tuple from the structured fields. Each
// key entry is resolved with dotted-path semantics, so it may be a codec alias
// ("stan") or a field path ("iso8583.field.11").
func (g *Gear) buildKey(msg *fluxmsg.FluxMsg) (string, error) {
	if msg == nil || msg.Data == nil {
		return "", fmt.Errorf("conductor: message has no structured data")
	}
	parts := make([]string, 0, len(g.corrKey))
	for _, f := range g.corrKey {
		v, ok := msg.Get(f)
		if !ok {
			return "", fmt.Errorf("conductor: correlation field %q missing", f)
		}
		parts = append(parts, fieldString(v))
	}
	return strings.Join(parts, keySep), nil
}

func (g *Gear) matchRoute(msg *fluxmsg.FluxMsg) *route {
	for _, r := range g.routes {
		if r.matches(msg) {
			return r
		}
	}
	return nil
}

// stripParked removes the parking-overlay fields from the outbound message;
// the ticket's retained request keeps the originals for restoration. Field
// names are dotted paths (a codec alias or "iso8583.field.N").
func (g *Gear) stripParked(msg *fluxmsg.FluxMsg) {
	if len(g.parkFields) == 0 || msg.Data == nil {
		return
	}
	for _, f := range g.parkFields {
		deletePath(msg.Data, f)
	}
}

// restoreParked reattaches the parked fields onto the reply, from the ticket's
// retained request.
func (g *Gear) restoreParked(t *valet.Ticket, reply *fluxmsg.FluxMsg) {
	if len(g.parkFields) == 0 || t.Request == nil {
		return
	}
	if reply.Data == nil {
		reply.Data = make(map[string]any, len(g.parkFields))
	}
	for _, f := range g.parkFields {
		if v, ok := t.Request.Get(f); ok {
			_ = reply.Set(f, v)
		}
	}
}

// deletePath removes a dotted-path leaf from a nested data structure, pruning
// empty parent maps it leaves behind. Levels may be map[string]any or
// map[any]any (a CBOR/YAML round trip can yield either), mirroring
// FluxMsg.Get/Set, so a parked field under a map[any]any parent is really
// removed from the outbound copy rather than silently left behind.
func deletePath(root map[string]any, path string) {
	deleteNested(root, strings.Split(path, "."))
}

// deleteNested deletes parts[0..] from container (a map[string]any or
// map[any]any) and returns whether container is empty afterwards.
func deleteNested(container any, parts []string) (empty bool) {
	if len(parts) == 1 {
		mapDelete(container, parts[0])
		return mapLen(container) == 0
	}
	child, ok := mapGet(container, parts[0])
	if !ok {
		return mapLen(container) == 0 // path absent; nothing to delete
	}
	if deleteNested(child, parts[1:]) {
		mapDelete(container, parts[0])
	}
	return mapLen(container) == 0
}

func mapGet(container any, key string) (any, bool) {
	switch m := container.(type) {
	case map[string]any:
		v, ok := m[key]
		return v, ok
	case map[any]any:
		v, ok := m[key]
		return v, ok
	default:
		return nil, false
	}
}

func mapDelete(container any, key string) {
	switch m := container.(type) {
	case map[string]any:
		delete(m, key)
	case map[any]any:
		delete(m, key)
	}
}

func mapLen(container any) int {
	switch m := container.(type) {
	case map[string]any:
		return len(m)
	case map[any]any:
		return len(m)
	default:
		return -1 // not a map: never prune
	}
}

// emitError annotates the original message with a reason and sends it out the
// error port. The conductor never authors a reply of its own.
func (g *Gear) emitError(msg *fluxmsg.FluxMsg, reason string) {
	if msg == nil {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string, 1)
	}
	msg.Metadata["error.reason"] = reason
	if err := g.emitter.Emit(portError, msg); err != nil {
		g.logger.Error("error emission failed", "reason", reason, "error", err)
	}
}

// Drain refuses new requests but keeps matching replies until open tickets
// clear or ctx expires.
func (g *Gear) Drain(ctx context.Context) error {
	if g.engine == nil {
		return nil
	}
	if err := g.engine.Drain(ctx); err != nil {
		return fmt.Errorf("conductor: %w", err)
	}
	return nil
}

// Stop releases the correlation engine. Idempotent.
func (g *Gear) Stop() error {
	if g.engine != nil {
		g.engine.Close()
	}
	return nil
}

// cloneMsg returns an independent copy of msg: Data and Metadata are
// deep-cloned and the Path slice is copied, so mutating the copy (strip, origin
// stamp, hop append) never touches the original. RawPayload is shared: it is
// treated as read-only on these paths. Used to keep the parked request and the
// retained outcome pristine and free of aliasing with the in-flight message.
func cloneMsg(msg *fluxmsg.FluxMsg) *fluxmsg.FluxMsg {
	if msg == nil {
		return nil
	}
	cp := *msg
	cp.Data = deepCloneMap(msg.Data)
	cp.Metadata = cloneMeta(msg.Metadata)
	if msg.Path != nil {
		cp.Path = append([]*fluxmsg.Hop(nil), msg.Path...)
	}
	return &cp
}

// deepCloneMap recursively clones a data map so nested field trees are not
// shared between the parked original and the outbound copy. It clones both
// map[string]any and map[any]any nested maps, mirroring FluxMsg.Get/Set (a
// CBOR/YAML round trip can yield either), so a parked field under a
// map[any]any parent is not silently shared.
func deepCloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch nested := v.(type) {
	case map[string]any:
		return deepCloneMap(nested)
	case map[any]any:
		cp := make(map[any]any, len(nested))
		for k, val := range nested {
			cp[k] = cloneValue(val)
		}
		return cp
	case []any:
		// A slice would otherwise be shared between the parked clone and the
		// mutated outbound copy; clone it (and its elements) so the two never
		// alias. ISO8583 fields are scalar, but generic FluxMsg data can nest
		// arrays.
		cp := make([]any, len(nested))
		for i, val := range nested {
			cp[i] = cloneValue(val)
		}
		return cp
	default:
		return v
	}
}

func cloneMeta(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// returnContext captures only the return-routing metadata a reply needs to be
// sent home: the origin connection id, peer, mesh origin stamp, and tracing.
// It deliberately excludes the protocol codec fields (iso8583.*, codec.*),
// which the matched reply supplies itself. Merging the return context over a
// reply must never clobber the reply's own MTI or fields with the request's.
func returnContext(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if strings.HasPrefix(k, "iso8583.") || strings.HasPrefix(k, "codec.") {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
