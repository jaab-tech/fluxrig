package bus

import (
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// Bus defines the standard behavior for our messaging layer.
// By using an interface, we can swap NATS for a memory mock during tests.
type Bus interface {
	// Connect establishes the connection to the mesh.
	Connect(url string, name string, connectTimeout time.Duration, reconnectWait time.Duration) error

	// Publish sends a FluxMsg to a specific subject (topic).
	// It should handle serialization internally.
	Publish(subject string, msg *fluxmsg.FluxMsg) error

	// Subscribe listens for messages on a subject.
	// It returns a subscription object (to allow Unsubscribe) and an error.
	Subscribe(subject string, handler Handler) (Subscription, error)

	// SubscribeDurable listens for messages using a persistent consumer (durableName).
	// This ensures messages are not lost/duplicated across restarts.
	SubscribeDurable(subject, durableName string, handler Handler) (Subscription, error)

	// Close cleans up the connection.
	Close()
}

// Handler is the function signature for processing incoming messages.
type Handler func(msg *fluxmsg.FluxMsg)

// Subscription represents an active listener.
type Subscription interface {
	Unsubscribe() error
}
