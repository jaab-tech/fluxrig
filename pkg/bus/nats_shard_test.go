// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNatsBus_Certification(t *testing.T) {
	b := NewNatsBus("test-stream")
	assert.NotNil(t, b)

	t.Run("Metadata", func(t *testing.T) {
		assert.Equal(t, "test-stream", b.streamName)
		assert.Nil(t, b.Core())
	})

	t.Run("OfflineFailures", func(t *testing.T) {
		ctx := context.Background()
		err := b.Publish(ctx, "test", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not connected")

		err = b.PublishRaw(ctx, "test", []byte("raw"), uuid.New())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not connected")

		_, err = b.Subscribe("test", nil)
		assert.Error(t, err)

		_, err = b.SubscribeRaw("test", "stream", nil)
		assert.Error(t, err)

		_, err = b.SubscribeDurable("test", "durable", nil)
		assert.Error(t, err)
	})

	t.Run("ConnectInvalid", func(t *testing.T) {
		opts := ConnectOptions{
			Name:           "test",
			ConnectTimeout: 100 * time.Millisecond,
		}
		// This should fail immediately
		err := b.Connect("nats://localhost:1", opts)
		assert.Error(t, err)
	})
}
