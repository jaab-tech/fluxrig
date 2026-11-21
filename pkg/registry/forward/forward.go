// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package forward carries control-plane writes to the Mixer that holds the
// write lease. A Mixer that does not hold it sends the write as a NATS
// request on a control subject and relays the reply, so a caller (an API
// handler, the enrollment controller, the scenario controller) sees no
// difference from a local write. Every Mixer subscribes, and only the
// holder answers (see Server).
//
// The lease itself is the fence (see kvstore.Lease); forwarding is how a
// non-holder's write reaches it. Every write is still revision-checked on the
// holder, so a stale forward cannot corrupt the registry.
package forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/bus"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// SubjectPrefix is where control writes are forwarded. The op name is
// appended, so the holder can subscribe to one op or all of them.
const SubjectPrefix = "flux.mixer.control."

// Op names. One per forwarded control write.
const (
	OpRegisterEntity     = "register_entity"
	OpApprove            = "approve"
	OpRemoveEntity       = "remove_entity"
	OpRemoveByName       = "remove_by_name"
	OpUpdateStatusEntity = "update_status_entity"
	OpPutScenario        = "put_scenario"
	OpSetActive          = "set_active"
	OpImportSpec         = "import_spec"
)

// Error codes that cross the hop. A caller maps the code back to the sentinel
// it already handles, so an API handler keeps its HTTP status.
const (
	CodeNameConflict     = "name_conflict"
	CodeNotFound         = "not_found"
	CodeBootstrapDenied  = "bootstrap_secret_mismatch"
	CodeNotLeaseHolder   = "not_lease_holder"
	CodeScenarioTooLarge = "scenario_too_large"
	CodeSpecImmutable    = "spec_immutable"
	CodeSpecTooLarge     = "spec_too_large"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// Request is one forwarded control write. Exactly one of the payload fields
// is set for a given Op. The struct is JSON so it is stable across versions
// during a rolling upgrade; a field a peer does not know is ignored, and an
// op a peer does not know is refused rather than guessed.
type Request struct {
	Op string `json:"op"`

	// RegisterEntity / UpsertSelf
	TypeID    uint8          `json:"type_id,omitempty"`
	MachineID uuid.UUID      `json:"machine_id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Secret    string         `json:"secret,omitempty"`
	IP        string         `json:"ip,omitempty"`
	Port      int            `json:"port,omitempty"`
	Version   string         `json:"version,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
	MixerID   uuid.UUID      `json:"mixer_id,omitempty"`

	// Approve
	ID      uuid.UUID `json:"id,omitempty"`
	NewName string    `json:"new_name,omitempty"`

	// UpdateStatus
	Status string `json:"status,omitempty"`

	// PutScenario / SetActive
	ScenarioName    string                   `json:"scenario_name,omitempty"`
	ScenarioContent []byte                   `json:"scenario_content,omitempty"`
	Active          *registry.ActiveScenario `json:"active,omitempty"`

	// ImportSpec
	SpecContent []byte `json:"spec_content,omitempty"`
	SpecName    string `json:"spec_name,omitempty"`
	SpecTag     string `json:"spec_tag,omitempty"`

	// TTLNanos is how long the caller waits for the reply, in nanoseconds
	// from when the request was sent. The holder applies no write past it:
	// a caller that already gave up (the forwarder default is 5s) must not
	// get the write applied anyway. Zero or negative means the holder's own
	// default.
	TTLNanos int64 `json:"ttl_nanos,omitempty"`
}

// Response is the reply. Rack is set for the ops that return one.
type Response struct {
	Rack *registry.Rack `json:"rack,omitempty"`
	// IssuedSecret carries Rack.Secret, which never serializes with the
	// Rack: the secret a fresh registration issued, for the Passport the
	// forwarding Mixer signs. Empty for every other op.
	IssuedSecret string `json:"issued_secret,omitempty"`
	// ImportSpec: what the holder stored.
	SpecHash  string `json:"spec_hash,omitempty"`
	SpecName  string `json:"spec_name,omitempty"`
	SpecTag   string `json:"spec_tag,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorText string `json:"error_text,omitempty"`
}

// ErrForbiddenNoHolder is returned to the caller when no Mixer holds the
// lease (or the holder did not answer within the timeout). The API maps it to
// 503: the write did not happen, and the caller may retry.
var ErrForbiddenNoHolder = errors.New("no Mixer holds the write lease; the control write was not applied")

// ErrUnavailable is returned when the holder could not apply a control write
// because JetStream could not answer yet: a metadata election, or no stream
// that answers. The API maps it to 503 like ErrForbiddenNoHolder: the caller
// may retry.
var ErrUnavailable = errors.New("the cluster could not apply the control write yet; retry")

// CodeOf maps a registry error to its wire code, or CodeInternal.
func CodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, registry.ErrNameConflict):
		return CodeNameConflict
	case errors.Is(err, registry.ErrNotFound):
		return CodeNotFound
	case errors.Is(err, registry.ErrBootstrapSecretMismatch):
		return CodeBootstrapDenied
	case errors.Is(err, registry.ErrNotLeaseHolder), errors.Is(err, ErrForbiddenNoHolder):
		return CodeNotLeaseHolder
	case errors.Is(err, manager.ErrVersionExists):
		return CodeSpecImmutable
	case errors.Is(err, manager.ErrTooLarge):
		return CodeSpecTooLarge
	case errors.Is(err, ErrUnavailable), bus.Unavailable(err):
		return CodeUnavailable
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "scenario larger than"):
		return CodeScenarioTooLarge
	}
	return CodeInternal
}

// ErrorOf maps a wire code back to the sentinel error the caller expects. An
// unknown or empty code with text becomes a plain error, never a silent nil.
func ErrorOf(code, text string) error {
	switch code {
	case "":
		return nil
	case CodeNameConflict:
		return registry.ErrNameConflict
	case CodeNotFound:
		return registry.ErrNotFound
	case CodeBootstrapDenied:
		return registry.ErrBootstrapSecretMismatch
	case CodeNotLeaseHolder:
		return ErrForbiddenNoHolder
	case CodeSpecImmutable:
		return fmt.Errorf("%w: %s", manager.ErrVersionExists, text)
	case CodeSpecTooLarge:
		return fmt.Errorf("%w: %s", manager.ErrTooLarge, text)
	case CodeUnavailable:
		return fmt.Errorf("%w: %s", ErrUnavailable, text)
	}
	if text == "" {
		text = "forwarded control write failed"
	}
	return fmt.Errorf("forwarded control write: %s", text)
}

// Transport is the request/reply channel a forwarder uses. RouterWrapper.NC
// satisfies it by wrapping nats.Conn.RequestWithContext; it is an interface so
// a test can drive it without a broker.
type Transport interface {
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)
}

// Forwarder sends a control write to the lease holder and relays the reply.
type Forwarder struct {
	transport Transport
	timeout   time.Duration
	log       *slog.Logger
}

// NewForwarder builds a forwarder. A non-positive timeout takes 5s, sized to
// cover a cross-site request plus the holder's own write.
func NewForwarder(transport Transport, timeout time.Duration, log *slog.Logger) *Forwarder {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Forwarder{transport: transport, timeout: timeout, log: log}
}

// Timeout is how long a forwarded write waits for the holder: the round trip
// to the cluster that a read of the lease also needs.
func (f *Forwarder) Timeout() time.Duration { return f.timeout }

// Do sends req and decodes the reply. A transport error or timeout is
// ErrForbiddenNoHolder: no holder answered, so the write did not happen.
func (f *Forwarder) Do(ctx context.Context, req Request) (Response, error) {
	// Config and Attrs come from the wire: a Rack's Hello is CBOR, which
	// decodes nested maps as map[any]any, and JSON refuses those. Without
	// this, every Hello that reached a Mixer without the lease failed to
	// forward, and the Rack never registered.
	req.Config, req.Attrs = registry.JSONSafeMap(req.Config), registry.JSONSafeMap(req.Attrs)
	// The holder must not apply a write the caller no longer waits for, so
	// the caller's deadline travels with the request: the smaller of the
	// caller's own deadline and the forward timeout.
	ttl := f.timeout
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < ttl {
		ttl = time.Until(dl)
	}
	req.TTLNanos = ttl.Nanoseconds()
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("forward: encode request: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	reply, err := f.transport.Request(rctx, SubjectPrefix+req.Op, body)
	if err != nil {
		f.log.Warn("control write had no lease holder to answer", "op", req.Op, "error", err)
		return Response{}, ErrForbiddenNoHolder
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return Response{}, fmt.Errorf("forward: decode reply: %w", err)
	}
	return resp, nil
}
