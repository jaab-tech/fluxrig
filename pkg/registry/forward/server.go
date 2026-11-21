// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package forward

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/jaab-tech/fluxrig/pkg/registry"
)

// Server answers forwarded control writes on the lease holder. Every Mixer
// subscribes to every op under SubjectPrefix, but only the one that holds
// the lease answers: a request/reply returns the first answer, so a
// non-holder that answered "not the holder" would win the race against the
// real holder. With no holder, nobody answers and the caller times out,
// which it reports as ErrForbiddenNoHolder.
type Server struct {
	reg    registry.Registry
	repo   ScenarioRepo
	specs  SpecImporter
	holder Holder
	log    *slog.Logger
	subs   []*nats.Subscription
}

// NewServer builds a holder-side server. repo may be nil for a caller that
// only forwards registry writes. holder may be nil, and then the server
// answers every request (a single Mixer, or a test).
func NewServer(reg registry.Registry, repo ScenarioRepo, holder Holder, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{reg: reg, repo: repo, holder: holder, log: log}
}

// WithSpecs lets the server answer forwarded spec imports. Without it an
// import is refused as if no Mixer held the lease.
func (s *Server) WithSpecs(specs SpecImporter) *Server {
	s.specs = specs
	return s
}

// Start subscribes to each op subject. It is a no-op for a nil connection, so
// a single Mixer that forwards nothing is unaffected.
func (s *Server) Start(nc *nats.Conn) error {
	if nc == nil {
		return nil
	}
	ops := []string{
		OpRegisterEntity, OpApprove, OpRemoveEntity, OpRemoveByName, OpUpdateStatusEntity,
		OpPutScenario, OpSetActive, OpImportSpec,
	}
	for _, op := range ops {
		subject := SubjectPrefix + op
		sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
			s.handle(m)
		})
		if err != nil {
			s.Stop()
			return fmt.Errorf("forward: subscribe %s: %w", subject, err)
		}
		s.subs = append(s.subs, sub)
	}
	return nil
}

// Stop unsubscribes every op subject.
func (s *Server) Stop() {
	for _, sub := range s.subs {
		_ = sub.Unsubscribe()
	}
	s.subs = nil
}

// holderWriteTimeout bounds one forwarded write on the holder when the
// caller named none (or a non-positive one): an old peer, or a test.
const holderWriteTimeout = 10 * time.Second

// handle executes one request and replies, but only on the holder. A
// request that reaches the holder always gets a reply: success, a typed
// error, or an internal error. A non-holder stays silent.
func (s *Server) handle(m *nats.Msg) {
	if s.holder != nil && !s.holder.Held() {
		return
	}
	var req Request
	if err := json.Unmarshal(m.Data, &req); err != nil {
		s.reply(m, Response{ErrorCode: CodeInternal, ErrorText: "decode request: " + err.Error()})
		return
	}
	// The write must not outlive the caller: when the caller's deadline is
	// past, the reply goes nowhere and the write must not be applied. The
	// bound is the smaller of the caller's TTL and the holder's own.
	timeout := holderWriteTimeout
	if req.TTLNanos > 0 && time.Duration(req.TTLNanos) < timeout {
		timeout = time.Duration(req.TTLNanos)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if req.Op == OpImportSpec {
		s.importSpec(ctx, m, req)
		return
	}
	rack, err := s.execute(ctx, req)
	if err != nil {
		s.log.Warn("forwarded control write failed", "op", req.Op, "error", err)
		s.reply(m, Response{ErrorCode: CodeOf(err), ErrorText: err.Error()})
		return
	}
	resp := Response{Rack: rack}
	if rack != nil {
		resp.IssuedSecret = rack.Secret
	}
	s.reply(m, resp)
}

// execute runs one op against the local registry.
func (s *Server) execute(ctx context.Context, req Request) (*registry.Rack, error) {
	switch req.Op {
	case OpRegisterEntity:
		return s.reg.RegisterEntity(ctx, req.TypeID, req.MachineID, req.Name, req.Secret, req.IP, req.Port, req.Version, req.Config, req.Attrs, req.MixerID)
	case OpApprove:
		return s.reg.Approve(ctx, req.ID, req.NewName)
	case OpRemoveEntity:
		return nil, s.reg.RemoveEntity(ctx, req.TypeID, req.ID)
	case OpRemoveByName:
		return nil, s.reg.RemoveByName(ctx, req.Name)
	case OpUpdateStatusEntity:
		return nil, s.reg.UpdateStatusEntity(ctx, req.TypeID, req.ID, req.Status)
	case OpPutScenario:
		if s.repo == nil {
			return nil, ErrForbiddenNoHolder
		}
		return nil, s.repo.PutScenario(ctx, req.ScenarioName, req.ScenarioContent)
	case OpSetActive:
		if s.repo == nil || req.Active == nil {
			return nil, ErrForbiddenNoHolder
		}
		return nil, s.repo.SetActive(ctx, *req.Active)
	default:
		return nil, fmt.Errorf("forward: unknown control op %q", req.Op)
	}
}

// importSpec runs a forwarded spec import and replies with what was stored.
func (s *Server) importSpec(ctx context.Context, m *nats.Msg, req Request) {
	if s.specs == nil {
		s.reply(m, Response{ErrorCode: CodeNotLeaseHolder, ErrorText: ErrForbiddenNoHolder.Error()})
		return
	}
	hash, name, tag, err := s.specs.ImportContent(ctx, req.SpecContent, req.SpecName, req.SpecTag)
	if err != nil {
		s.log.Warn("forwarded spec import failed", "spec", req.SpecName, "error", err)
		s.reply(m, Response{ErrorCode: CodeOf(err), ErrorText: err.Error()})
		return
	}
	s.reply(m, Response{SpecHash: hash, SpecName: name, SpecTag: tag})
}

func (s *Server) reply(m *nats.Msg, resp Response) {
	body, err := json.Marshal(resp)
	if err != nil {
		body = []byte(`{"error_code":"internal","error_text":"encode reply"}`)
	}
	_ = m.Respond(body)
}
