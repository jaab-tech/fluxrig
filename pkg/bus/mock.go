package bus

import (
	"sync"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
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

func (m *MockBus) Connect(url string, name string, connectTimeout time.Duration, reconnectWait time.Duration) error {
	return nil
}

func (m *MockBus) Publish(subject string, msg *fluxmsg.FluxMsg) error {
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
		go handler(msg)
		return nil
	}

	// 2. Simple Wildcard Match (suffix ">")
	for sub, handler := range handlers {
		if len(sub) > 1 && sub[len(sub)-1] == '>' {
			prefix := sub[:len(sub)-1]
			if len(subject) >= len(prefix) && subject[:len(prefix)] == prefix {
				go handler(msg)
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
