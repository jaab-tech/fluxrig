// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestWatermillMiddleware(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("test")
	metrics, _ := NewMetrics(meter)
	mw := NewWatermillMiddleware(metrics)

	handlerCalled := false
	handler := func(msg *message.Message) ([]*message.Message, error) {
		handlerCalled = true
		return nil, nil
	}

	wrapped := mw.Middleware(handler)
	msg := message.NewMessage("123", []byte("payload"))

	msgs, err := wrapped(msg)
	require.NoError(t, err)
	require.Nil(t, msgs)
	require.True(t, handlerCalled)
}

func TestWatermillMiddleware_NilMetrics(t *testing.T) {
	mw := NewWatermillMiddleware(nil)

	handlerCalled := false
	handler := func(msg *message.Message) ([]*message.Message, error) {
		handlerCalled = true
		return nil, nil
	}

	wrapped := mw.Middleware(handler)
	msg := message.NewMessage("123", []byte("payload"))

	msgs, err := wrapped(msg)
	require.NoError(t, err)
	require.Nil(t, msgs)
	require.True(t, handlerCalled)
}
