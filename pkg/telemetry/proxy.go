// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"log/slog"
	"sync"
)

type sharedState struct {
	mu     sync.RWMutex
	root   slog.Handler
	serial uint64
}

// SwitchableHandler allows swapping the underlying handler at runtime.
// All loggers using this handler (including those created with WithAttrs/WithGroup)
// will automatically use the new target handler.
type SwitchableHandler struct {
	shared     *sharedState
	lastSerial uint64
	target     slog.Handler
	attrs      []slog.Attr
	groups     []string
}

func NewSwitchableHandler(target slog.Handler) *SwitchableHandler {
	return &SwitchableHandler{
		shared: &sharedState{
			root: target,
		},
		target: target,
	}
}

func (s *SwitchableHandler) Switch(newTarget slog.Handler) {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()

	s.shared.root = newTarget
	s.shared.serial++
}

func (s *SwitchableHandler) getHandler() slog.Handler {
	s.shared.mu.RLock()
	if s.target != nil && s.lastSerial == s.shared.serial {
		defer s.shared.mu.RUnlock()
		return s.target
	}
	s.shared.mu.RUnlock()

	// Update needed
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()

	// Double check
	if s.lastSerial == s.shared.serial {
		return s.target
	}

	t := s.shared.root
	for _, g := range s.groups {
		t = t.WithGroup(g)
	}
	if len(s.attrs) > 0 {
		t = t.WithAttrs(s.attrs)
	}
	s.target = t
	s.lastSerial = s.shared.serial
	return s.target
}

func (s *SwitchableHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return s.getHandler().Enabled(ctx, l)
}

func (s *SwitchableHandler) Handle(ctx context.Context, r slog.Record) error {
	return s.getHandler().Handle(ctx, r)
}

func (s *SwitchableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := append(append([]slog.Attr{}, s.attrs...), attrs...)
	return &SwitchableHandler{
		shared:     s.shared,
		attrs:      newAttrs,
		groups:     append([]string{}, s.groups...),
		target:     s.getHandler().WithAttrs(attrs),
		lastSerial: s.shared.serial,
	}
}

func (s *SwitchableHandler) WithGroup(name string) slog.Handler {
	newGroups := append(append([]string{}, s.groups...), name)
	return &SwitchableHandler{
		shared:     s.shared,
		attrs:      append([]slog.Attr{}, s.attrs...),
		groups:     newGroups,
		target:     s.getHandler().WithGroup(name),
		lastSerial: s.shared.serial,
	}
}
