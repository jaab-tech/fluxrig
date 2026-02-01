// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bus

import (
	"context"
	"sync"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/vmihailenco/msgpack/v5"
)

// MockBus is a memory-based implementation for testing.
type MockBus struct {
	PublishedMessages map[string][]*fluxmsg.FluxMsg
	Handlers          map[string]Handler
	mu                sync.RWMutex
}

func NewMockBus() *MockBus {
	return &MockBus{
		PublishedMessages: make(map[string][]*fluxmsg.FluxMsg),
		Handlers:          make(map[string]Handler),
	}
}

func (m *MockBus) Connect(url string, opts ConnectOptions) error {
	return nil
}

// PublishRaw implements PublishRaw by unmarshaling and calling Publish (simulating wire).
func (m *MockBus) PublishRaw(ctx context.Context, subject string, data []byte, fluxID uint64) error {
	var msg fluxmsg.FluxMsg
	if err := msgpack.Unmarshal(data, &msg); err != nil {
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

func (m *MockBus) Close() {}

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
