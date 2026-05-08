// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// MockBus is a memory-based implementation for testing.
type MockBus struct {
	PublishedMessages map[string][]*fluxmsg.FluxMsg
	Handlers          map[string]Handler
	kv                *MockKeyValue
	mu                sync.RWMutex
}

func NewMockBus() *MockBus {
	return &MockBus{
		PublishedMessages: make(map[string][]*fluxmsg.FluxMsg),
		Handlers:          make(map[string]Handler),
		kv: &MockKeyValue{
			data: make(map[string]map[string][]byte),
		},
	}
}

func (m *MockBus) Connect(url string, opts ConnectOptions) error {
	return nil
}

// PublishRaw implements PublishRaw by unmarshaling and calling Publish (simulating wire).
func (m *MockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uuid.UUID) error {
	var msg fluxmsg.FluxMsg
	if err := cbor.Unmarshal(data, &msg); err != nil {
		return err
	}
	// Verify ID matches if needed, but for mock just publish
	return m.Publish(ctx, subject, &msg)
}

func (m *MockBus) Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error {
	m.mu.Lock()
	m.PublishedMessages[subject] = append(m.PublishedMessages[subject], msg)
	handlers := make(map[string]Handler, len(m.Handlers))
	for k, v := range m.Handlers {
		handlers[k] = v
	}
	m.mu.Unlock()

	// Direct Loopback for testing handlers
	// 1. Exact Match
	if handler, ok := handlers[subject]; ok {
		go handler(ctx, msg)
		return nil
	}

	// 2. Simple Wildcard Match (suffix ">")
	for sub, handler := range handlers {
		if len(sub) > 1 && sub[len(sub)-1] == '>' {
			prefix := sub[:len(sub)-1]
			if len(subject) >= len(prefix) && subject[:len(prefix)] == prefix {
				go handler(ctx, msg)
			}
		}
	}
	return nil
}

func (m *MockBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Handlers[subject] = handler
	return &MockSubscription{}, nil
}

func (m *MockBus) SubscribeDurable(subject, durableName string, handler Handler) (Subscription, error) {
	// Mock treats durable same as normal for testing
	return m.Subscribe(subject, handler)
}

func (m *MockBus) SubscribeRaw(subject string, streamName string, handler RawHandler) (Subscription, error) {
	// For mock, we can assume normal subscribe behavior, ignoring streamName
	return &MockSubscription{}, nil
}

func (m *MockBus) Core() any { return nil }

func (m *MockBus) Close() {}

func (m *MockBus) KV() KeyValue {
	if m.kv == nil {
		m.kv = &MockKeyValue{
			data: make(map[string]map[string][]byte),
		}
	}
	return m.kv
}

// GetMessages safely retrieves messages for a subject
func (m *MockBus) GetMessages(subject string) []*fluxmsg.FluxMsg {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to avoid race on slice access
	msgs := m.PublishedMessages[subject]
	if msgs == nil {
		return nil
	}
	res := make([]*fluxmsg.FluxMsg, len(msgs))
	copy(res, msgs)
	return res
}

type MockSubscription struct{}

func (s *MockSubscription) Unsubscribe() error {
	return nil
}

type MockKeyValue struct {
	data map[string]map[string][]byte
	mu   sync.RWMutex
}

func (m *MockKeyValue) Put(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string]map[string][]byte)
	}
	if m.data[bucket] == nil {
		m.data[bucket] = make(map[string][]byte)
	}
	m.data[bucket][key] = value
	return 1, nil
}

func (m *MockKeyValue) Get(ctx context.Context, bucket, key string) ([]byte, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data == nil {
		return nil, 0, nil
	}
	if m.data[bucket] == nil {
		return nil, 0, nil
	}
	val, ok := m.data[bucket][key]
	if !ok {
		return nil, 0, nil
	}
	return val, 1, nil
}

func (m *MockKeyValue) Delete(ctx context.Context, bucket, key string) error { return nil }
func (m *MockKeyValue) Watch(ctx context.Context, bucket, keys string, handler KVHandler) (Subscription, error) {
	return &MockSubscription{}, nil
}
func (m *MockKeyValue) Keys(ctx context.Context, bucket string) ([]string, error) { return nil, nil }
func (m *MockKeyValue) EnsureBucket(ctx context.Context, bucket string, storage string, replicas int, ttl time.Duration) error {
	return nil
}
