// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package valet

import (
	"context"
	"sync"
)

// MemoryStore is the default Store: a mutex-guarded map, RAM only, nothing on
// disk. It is the right choice when replies are connection-bound to the owning
// instance and clients retransmit on timeout; state does not survive a restart.
type MemoryStore struct {
	mu      sync.RWMutex
	tickets map[string]*Ticket
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tickets: make(map[string]*Ticket)}
}

// Insert implements Store.
func (s *MemoryStore) Insert(_ context.Context, t *Ticket) (*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.tickets[t.Key]; ok {
		return existing, nil
	}
	s.tickets[t.Key] = t
	return nil, nil
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, key string) (*Ticket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tickets[key]
	if !ok {
		return nil, ErrTicketNotFound
	}
	return t, nil
}

// Take implements Store.
func (s *MemoryStore) Take(_ context.Context, key string) (*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[key]
	if !ok {
		return nil, ErrTicketNotFound
	}
	delete(s.tickets, key)
	return t, nil
}

// Purge implements Store.
func (s *MemoryStore) Purge(_ context.Context) ([]*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Ticket, 0, len(s.tickets))
	for _, t := range s.tickets {
		out = append(out, t)
	}
	s.tickets = make(map[string]*Ticket)
	return out, nil
}

// Len implements Store.
func (s *MemoryStore) Len(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tickets), nil
}
