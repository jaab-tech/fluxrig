// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// blockingJS is a JetStream whose account info never answers, the way a
// request to a black-holed server sits in a blocked socket write.
type blockingJS struct {
	jetstream.JetStream
	release chan struct{}
}

func (b blockingJS) AccountInfo(context.Context) (*jetstream.AccountInfo, error) {
	<-b.release
	return nil, context.DeadlineExceeded
}

// The failover probe must give up when its own context ends, even when the
// request is stuck. nats.go bounds a write by its flusher timeout of one
// minute, so without the bound the probe holds the connection past the 60 s
// that gate 1 allows.
func TestNatsBus_AccountInfoBoundedGivesUpWithContext(t *testing.T) {
	release := make(chan struct{})
	nb := &NatsBus{js: blockingJS{release: release}}
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := nb.accountInfoBounded(ctx)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 2*time.Second, "the bound must not wait for the stuck request")
}
