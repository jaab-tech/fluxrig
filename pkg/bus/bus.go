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
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// ConnectOptions holds parameters for establishing a connection.
type ConnectOptions struct {
	Name           string
	ConnectTimeout time.Duration
	ReconnectWait  time.Duration
	RootCA         string
}

// Bus defines the standard behavior for our messaging layer.
// By using an interface, we can swap NATS for a memory mock during tests.
type Bus interface {
	// Connect establishes the connection to the mesh.
	Connect(url string, opts ConnectOptions) error

	// Publish sends a FluxMsg to a specific subject (topic).
	// It must respect the context for cancellation and tracing propagation.
	Publish(ctx context.Context, subject string, msg *fluxmsg.FluxMsg) error

	// PublishRaw sends pre-serialized data (MsgPack) with a specific deduplication ID.
	PublishRaw(ctx context.Context, subject string, data []byte, fluxID uint64) error

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
// The context contains tracing information extracted from the message.
type Handler func(ctx context.Context, msg *fluxmsg.FluxMsg)

// Subscription represents an active listener.
type Subscription interface {
	Unsubscribe() error
}
